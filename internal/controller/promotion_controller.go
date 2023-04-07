/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/fluxcd/go-git-providers/gitprovider"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	securejoin "github.com/cyphar/filepath-securejoin"
	promotionsv1alpha1 "github.com/thomasstxyz/gitops-promotions-operator/api/v1alpha1"
	"github.com/thomasstxyz/gitops-promotions-operator/internal/util"
)

// PromotionReconciler reconciles a Promotion object
type PromotionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=promotions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=promotions/finalizers,verbs=update

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	start := time.Now()

	log.Info("Begin reconciling Promotion", "name", req.NamespacedName)

	obj := &promotionsv1alpha1.Promotion{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Run these functions after the reconcile loop
	defer func() {
		obj.Status.ObservedGeneration = obj.GetObjectMeta().GetGeneration()

		if err := r.Status().Update(ctx, obj); err != nil {
			log.Error(err, "Unable to update Promotion status")
		}
	}()

	// Get source and target environments
	sourceEnvironment := &promotionsv1alpha1.Environment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: obj.Namespace, Name: obj.Spec.SourceEnvironmentRef.Name}, sourceEnvironment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	targetEnvironment := &promotionsv1alpha1.Environment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: obj.Namespace, Name: obj.Spec.TargetEnvironmentRef.Name}, targetEnvironment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ensure that the source and target environments are ready
	if !sourceEnvironment.IsReady() {
		log.Error(nil, "Source environment is not ready", "sourceEnvironment", sourceEnvironment)
		return ctrl.Result{}, nil
	}
	if !targetEnvironment.IsReady() {
		log.Error(nil, "Target environment is not ready", "targetEnvironment", targetEnvironment)
		return ctrl.Result{}, nil
	}

	// Clone source environment repo
	tmpDir, err := util.TempDirForObj("", obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer os.RemoveAll(tmpDir)
	sourceEnvironmentRepo, err := GitCloneEnvironment(ctx, r.Client, sourceEnvironment, tmpDir)
	if err != nil {
		return ctrl.Result{}, err
	}
	sourceEnvironmentPath := tmpDir

	// Clone target environment repo
	tmpDir, err = util.TempDirForObj("", obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer os.RemoveAll(tmpDir)
	targetEnvironmentRepo, err := GitCloneEnvironment(ctx, r.Client, targetEnvironment, tmpDir)
	if err != nil {
		return ctrl.Result{}, err
	}
	targetEnvironmentPath := tmpDir

	// Get the GitProviderRepo for the target environment
	targetEnvironmentGitProviderRepo, err := NewGitProviderOrgRepository(ctx, r.Client, targetEnvironment, targetEnvironmentRepo)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Get the git worktree for the source and target environment repos
	_, err = sourceEnvironmentRepo.Worktree()
	if err != nil {
		return ctrl.Result{}, err
	}
	targetEnvironmentWorktree, err := targetEnvironmentRepo.Worktree()
	if err != nil {
		return ctrl.Result{}, err
	}

	sourceEnvironmentRepoHeadRef, err := sourceEnvironmentRepo.Head()
	if err != nil {
		return ctrl.Result{}, err
	}

	// Get the source environment's latest git commit
	sourceEnvironmentLatestCommit, err := sourceEnvironmentRepo.CommitObject(sourceEnvironmentRepoHeadRef.Hash())
	if err != nil {
		return ctrl.Result{}, err
	}

	gitAuthOpts, cloneURL, err := SetupGitAuthEnvironment(ctx, r.Client, targetEnvironment)
	if err != nil {
		return ctrl.Result{}, err
	}

	prs, err := targetEnvironmentGitProviderRepo.PullRequests().List(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// isPROpen tells us whether there's already an open pull request for this promotion
	var isPROpen bool
	if obj.Status.LastPullRequestNumber != 0 {
		for _, pr := range prs {
			if pr.Get().Number == obj.Status.LastPullRequestNumber {
				isPROpen = true
				break
			}
		}
	}

	var pr gitprovider.PullRequest
	var branch string
	if isPROpen {
		pr, err = targetEnvironmentGitProviderRepo.PullRequests().Get(ctx, obj.Status.LastPullRequestNumber)
		if err != nil {
			return ctrl.Result{}, err
		}

		branch = pr.Get().SourceBranch

		if err := targetEnvironmentRepo.Fetch(&gogit.FetchOptions{
			RefSpecs:  []config.RefSpec{"refs/*:refs/*", "HEAD:refs/heads/HEAD"},
			Auth:      gitAuthOpts,
			RemoteURL: cloneURL,
		}); err != nil {
			return ctrl.Result{}, err
		}

		if err = targetEnvironmentWorktree.Checkout(&gogit.CheckoutOptions{
			Branch: plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branch)),
			Force:  true,
		}); err != nil {
			return ctrl.Result{}, err
		}
	} else if !isPROpen {
		branch = fmt.Sprintf("promotion/%s-%s", obj.Name, time.Now().Format("2006-01-02-15-04-05"))

		if err := targetEnvironmentWorktree.Checkout(&gogit.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(branch),
			Create: true,
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := CopyOperations(ctx, r.Client, obj, sourceEnvironment, targetEnvironment, sourceEnvironmentPath, targetEnvironmentPath); err != nil {
		return ctrl.Result{}, err
	}

	if err := targetEnvironmentWorktree.AddGlob("."); err != nil {
		return ctrl.Result{}, err
	}

	// Template commit message.
	type TemplateData struct {
		Prom                          *promotionsv1alpha1.Promotion
		SourceEnv                     *promotionsv1alpha1.Environment
		TargetEnv                     *promotionsv1alpha1.Environment
		SourceEnvironmentLatestCommit string
	}
	tplData := TemplateData{obj, sourceEnvironment, targetEnvironment, sourceEnvironmentLatestCommit.Hash.String()[0:7]}

	// TODO: See which Copy Operations (Promotion subjects) actually resulted in changes.
	//   Could be done by changing CopyOperations() to CopyOperation() and running git diff after every CopyOperation().

	tmpl, err := template.New("tpl").Parse(
`chore: promote {{.SourceEnvironmentLatestCommit}} from {{.SourceEnv.Name}} to {{.TargetEnv.Name}}

Promotion subjects:
====================
{{range .Prom.Spec.Copy}}- {{.Name}}
{{end}}
`)

	if err != nil {
		return ctrl.Result{}, err
	}
	var tpl bytes.Buffer
	err = tmpl.Execute(&tpl, tplData)
	if err != nil {
		return ctrl.Result{}, err
	}
	commitMsg := tpl.String()

	status, err := targetEnvironmentWorktree.Status()
	if err != nil {
		return ctrl.Result{}, err
	}

	if !status.IsClean() {
		_, err := targetEnvironmentWorktree.Commit(commitMsg,
			&gogit.CommitOptions{
				Author: &object.Signature{
					Name:  "Promotion Bot",
					Email: "bot@promotions.gitopsprom.io",
					When:  time.Now(),
				},
			})
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := targetEnvironmentRepo.Push(&gogit.PushOptions{
			RemoteName: "origin",
			RemoteURL:  cloneURL,
			Auth:       gitAuthOpts,
		}); err != nil {
			return ctrl.Result{}, err
		}

		*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "Pushed new changes to PR branch")

		if !isPROpen {
			title := fmt.Sprintf("chore: promote changes from %s to %s", sourceEnvironment.Name, targetEnvironment.Name)

			pr, err = targetEnvironmentGitProviderRepo.PullRequests().Create(ctx, title, branch, targetEnvironment.Spec.Source.Reference.Branch, "")
			if err != nil {
				return ctrl.Result{}, err
			}
			isPROpen = true

			*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "New Pull request created successfully")

			obj.Status.LastPullRequestNumber = pr.Get().Number
			obj.Status.LastPullRequestURL = pr.Get().WebURL
		}
		// else if isPROpen {
			// TODO: Edit (update) PR title. With current gitprovider API, we can only edit the PR title.
		// }
	} else if status.IsClean() {
		*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "A pull request is open for review.")
	}

	// If there's no open PR at this point, we assume that the source and target environments are in sync.
	if !isPROpen {
		*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "Source and target environments are in sync, nothing to promote.")
	}

	end := time.Now()
	log.Info("Reconciled Promotion successfully", "duration", end.Sub(start))

	return ctrl.Result{}, nil
}

// GetCommitObject returns the commit object for a given commit hash
func GetCommitObject(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Promotion, repo *gogit.Repository, branch string, commitHash plumbing.Hash) (*object.Commit, error) {
	ref := plumbing.NewHashReference(plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branch)), commitHash)

	commit, err := object.GetCommit(storer.EncodedObjectStorer(repo.Storer), ref.Hash())
	if err != nil {
		return nil, err
	}

	return commit, nil
}

func CopyOperations(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Promotion, sourceEnvironment *promotionsv1alpha1.Environment, targetEnvironment *promotionsv1alpha1.Environment, sourceEnvironmentPath string, targetEnvironmentPath string) error {
	sourceEnvironmentPath = filepath.Join(sourceEnvironmentPath, sourceEnvironment.Spec.Path)
	targetEnvironmentPath = filepath.Join(targetEnvironmentPath, targetEnvironment.Spec.Path)

	for _, cp := range obj.Spec.Copy {
		s, err := securejoin.SecureJoin(sourceEnvironmentPath, cp.Source)
		if err != nil {
			return err
		}
		t, err := securejoin.SecureJoin(targetEnvironmentPath, cp.Target)
		if err != nil {
			return err
		}

		cmd := exec.Command("cp", "-r", s, t)

		_, err = cmd.Output()
		if err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&promotionsv1alpha1.Promotion{}).
		Complete(r)
}
