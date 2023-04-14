/*
Copyright 2023 Thomas Stadler <thomas@thomasst.xyz>

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
	"path/filepath"
	"strings"
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
	"github.com/thomasstxyz/gitops-promotions-operator/internal/fs"
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
		log.Info("Waiting for source environment to get ready", "sourceEnvironment", sourceEnvironment, "requeueAfter", "10s")
		return ctrl.Result{
			RequeueAfter: 10 * time.Second,
		}, nil
	}
	if !targetEnvironment.IsReady() {
		log.Info("Waiting for target environment to get ready", "targetEnvironment", targetEnvironment, "requeueAfter", "10s")
		return ctrl.Result{
			RequeueAfter: 10 * time.Second,
		}, nil
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
	// targetEnvironmentRepoHeadRef, err := targetEnvironmentRepo.Head()
	// if err != nil {
	// 	return ctrl.Result{}, err
	// }

	// Get the source environment's latest git commit
	sourceEnvironmentLatestCommit, err := sourceEnvironmentRepo.CommitObject(sourceEnvironmentRepoHeadRef.Hash())
	if err != nil {
		return ctrl.Result{}, err
	}
	// Get the target environment's latest git commit
	// targetEnvironmentLatestCommit, err := targetEnvironmentRepo.CommitObject(targetEnvironmentRepoHeadRef.Hash())
	// if err != nil {
	// 	return ctrl.Result{}, err
	// }

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

	var promotedSubjects []string

	beforeHeadRef, err := targetEnvironmentRepo.Head()
	if err != nil {
		return ctrl.Result{}, err
	}

	// Copy the promotion subjects from the source environment to the target environment

	sourceEnvironmentFullPath := filepath.Join(sourceEnvironmentPath, sourceEnvironment.Spec.Path)
	targetEnvironmentFullPath := filepath.Join(targetEnvironmentPath, targetEnvironment.Spec.Path)

	for _, copyOperation := range obj.Spec.Copy {
		copySource, err := securejoin.SecureJoin(sourceEnvironmentFullPath, copyOperation.Source)
		if err != nil {
			return ctrl.Result{}, err
		}
		copyTarget, err := securejoin.SecureJoin(targetEnvironmentFullPath, copyOperation.Target)
		if err != nil {
			return ctrl.Result{}, err
		}

		if err := CopyOperation(ctx, obj, copySource, copyTarget); err != nil {
			return ctrl.Result{}, err
		}

		var status gogit.Status
		status, err = targetEnvironmentWorktree.Status()
		if err != nil {
			return ctrl.Result{}, err
		}

		if status.IsClean() {
			// fmt.Println("No changes were made by this copy operation.")
		} else {
			// fmt.Println("Changes were made by this copy operation.")

			// Add all files to the target environment git worktree
			if err := targetEnvironmentWorktree.AddGlob("."); err != nil {
				return ctrl.Result{}, err
			}

			// Template commit message.
			type TemplateData struct {
				Prom                          *promotionsv1alpha1.Promotion
				SourceEnv                     *promotionsv1alpha1.Environment
				TargetEnv                     *promotionsv1alpha1.Environment
				SourceEnvironmentLatestCommit string
				CopyOperation                 promotionsv1alpha1.CopyOperation
			}
			tplData := TemplateData{obj, sourceEnvironment, targetEnvironment, sourceEnvironmentLatestCommit.Hash.String()[0:7], copyOperation}

			tmpl, err := template.New("tpl").Parse(
				`chore: promote {{.CopyOperation.Name}} from {{.SourceEnv.Name}} to {{.TargetEnv.Name}}

SHA in source environment: {{.SourceEnvironmentLatestCommit}}
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

			_, err = targetEnvironmentWorktree.Commit(commitMsg,
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

			promotedSubjects = append(promotedSubjects, copyOperation.Name)

			*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "Pushed new commits to PR branch")
		}
	}

	afterHeadRef, err := targetEnvironmentRepo.Head()
	if err != nil {
		return ctrl.Result{}, err
	}

	// If we introduced new commits
	if beforeHeadRef.Hash().String() != afterHeadRef.Hash().String() {
		promotedSubjectsFormatted := strings.Join(promotedSubjects, ", ")
		prTitle := fmt.Sprintf("chore: promote %s from %s to %s", promotedSubjectsFormatted, sourceEnvironment.Name, targetEnvironment.Name)

		if isPROpen {
			_, err = targetEnvironmentGitProviderRepo.PullRequests().Edit(ctx, pr.Get().Number, gitprovider.EditOptions{
				Title: &prTitle,
			})
			if err != nil {
				return ctrl.Result{}, err
			}
		} else {
			pr, err = targetEnvironmentGitProviderRepo.PullRequests().Create(ctx, prTitle, branch, targetEnvironment.Spec.Source.Reference.Branch, "")
			if err != nil {
				return ctrl.Result{}, err
			}
			isPROpen = true

			log.Info("Created new pull request", "WebURL", pr.Get().WebURL)
			*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "New Pull request created successfully")

			obj.Status.LastPullRequestNumber = pr.Get().Number
			obj.Status.LastPullRequestURL = pr.Get().WebURL
		}
	} else {
		*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "A pull request is open for review.")
	}

	// If there's no open PR at this point, we assume that the source and target environments are in sync.
	if !isPROpen {
		*obj = promotionsv1alpha1.PromotionReady(*obj, promotionsv1alpha1.SucceededReason, "Source and target environments are in sync, nothing to promote.")
	}

	end := time.Now()
	log.Info("Reconciled Promotion successfully", "duration", end.Sub(start), "nextReconcile", "300s")

	return ctrl.Result{
		RequeueAfter: 300 * time.Second,
	}, nil
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

func CopyOperation(ctx context.Context, obj *promotionsv1alpha1.Promotion,
	copySource string, copyTarget string) error {

	if !fs.Exists(copySource) {
		return fmt.Errorf("source path %s does not exist", copySource)
	}

	copySourceFileInfo, err := os.Stat(copySource)
	if err != nil {
		return err
	}
	copyTargetFileInfo, err := os.Stat(copyTarget)
	if err != nil {
		return err
	}

	// If source is a directory.
	if copySourceFileInfo.IsDir() {
		// Create target directory if it does not exist.
		if err := os.MkdirAll(filepath.Dir(copyTarget), 0755); err != nil {
			return err
		}

		if err := fs.CopyDirectory(copySource, copyTarget); err != nil {
			return err
		}
		// If source is a file.
	} else {
		// Handle case when specified target is a directory.
		if copyTargetFileInfo.IsDir() {
			copyTarget = filepath.Join(copyTarget, filepath.Base(copySource))
		}

		// Create target directory if it does not exist.
		if err := os.MkdirAll(filepath.Dir(copyTarget), 0755); err != nil {
			return err
		}

		if err := fs.Copy(copySource, copyTarget); err != nil {
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
