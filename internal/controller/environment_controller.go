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
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/fluxcd/go-git-providers/github"
	"github.com/fluxcd/go-git-providers/gitprovider"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"

	promotionsv1alpha1 "github.com/thomasstxyz/gitops-promotions-operator/api/v1alpha1"
	"github.com/thomasstxyz/gitops-promotions-operator/internal/util"
)

// EnvironmentReconciler reconciles a Environment object
type EnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=environments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=environments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=promotions.gitopsprom.io,resources=environments/finalizers,verbs=update

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	start := time.Now()

	obj := &promotionsv1alpha1.Environment{}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Run these functions after the reconcile loop
	defer func() {
		obj.Status.ObservedGeneration = obj.GetObjectMeta().GetGeneration()

		if err := r.Status().Update(ctx, obj); err != nil {
			log.Error(err, "Unable to update Environment status")
		}
	}()

	// Check if we can clone the repository

	tmpDir, err := util.TempDirForObj("", obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer os.RemoveAll(tmpDir)

	repo, err := GitCloneEnvironment(ctx, r.Client, obj, tmpDir)
	if err != nil {
		return ctrl.Result{}, err
	}

	_, err = repo.Worktree()
	if err != nil {
		return ctrl.Result{}, err
	}
	head, err := repo.Head()
	if err != nil {
		return ctrl.Result{}, err
	}
	commit := head.Hash()

	// If we reach this far, we assume that the environment is ready

	*obj = promotionsv1alpha1.EnvironmentReady(*obj, promotionsv1alpha1.SucceededReason, "Authentication works, cloned repo successfully.", commit.String())

	end := time.Now()
	log.Info("Reconciled Environment successfully", "duration", end.Sub(start))

	return ctrl.Result{}, nil
}

func SetupGitAuthEnvironment(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Environment) (gitAuthOpts transport.AuthMethod, cloneURL string, err error) {
	cloneURL = obj.Spec.Source.URL

	// If we have a secret, we use SSH with auth options to clone the repository
	if obj.Spec.Source.SecretRef != nil {
		sshSecret := &corev1.Secret{}
		if err := client.Get(ctx, types.NamespacedName{Name: obj.Spec.Source.SecretRef.Name, Namespace: obj.Namespace}, sshSecret); err != nil {
			return gitAuthOpts, cloneURL, err
		}

		sshSigner, err := ssh.ParsePrivateKey(sshSecret.Data["private"])
		if err != nil {
			return gitAuthOpts, cloneURL, err
		}
		gitAuthOpts = &gogitssh.PublicKeys{
			User:   "git",
			Signer: sshSigner,
			HostKeyCallbackHelper: gogitssh.HostKeyCallbackHelper{
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			},
		}
		cloneURL = strings.Replace(cloneURL, "https://", "git@", 1)
		cloneURL = strings.Replace(cloneURL, ".com/", ".com:", 1)
	}

	return gitAuthOpts, cloneURL, nil
}

func GitCloneEnvironment(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Environment, tmpDir string) (*gogit.Repository, error) {
	gitAuthOpts, cloneURL, err := SetupGitAuthEnvironment(ctx, client, obj)
	if err != nil {
		return nil, err
	}

	repo, err := gogit.PlainClone(tmpDir, false, &gogit.CloneOptions{
		URL:           cloneURL,
		ReferenceName: plumbing.NewBranchReferenceName(obj.GetBranch()),
		Auth:          gitAuthOpts,
	})
	if err != nil {
		return nil, err
	}

	return repo, nil
}

func GitCommitEnvironment(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Environment, tmpDir string) (*gogit.Repository, error) {
	return nil, nil
}

func NewGitProviderOrgRepository(ctx context.Context, client client.Client, obj *promotionsv1alpha1.Environment, repo *gogit.Repository) (gitprovider.OrgRepository, error) {
	var c gitprovider.Client

	tokenSecret := &corev1.Secret{}
	if obj.Spec.ApiTokenSecretRef != nil {
		err := client.Get(ctx, types.NamespacedName{Name: obj.Spec.ApiTokenSecretRef.Name, Namespace: obj.Namespace}, tokenSecret)
		if err != nil {
			return nil, err
		}
	}
	token := string(tokenSecret.Data["token"])

	switch obj.Spec.GitProvider {
	case promotionsv1alpha1.GitProviderGitHub:
		var err error
		c, err = github.NewClient(gitprovider.WithOAuth2Token(token))
		if err != nil {
			return nil, err
		}
	default:
		fmt.Println("No Git Provider specified")
	}

	// Parse the URL into an OrgRepositoryRef
	ref, err := gitprovider.ParseOrgRepositoryURL(obj.Spec.Source.URL)
	if err != nil {
		return nil, err
	}
	// Get public information about the git repository.
	gitProviderRepo, err := c.OrgRepositories().Get(ctx, *ref)
	if err != nil {
		return nil, err
	}

	return gitProviderRepo, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&promotionsv1alpha1.Environment{}).
		Complete(r)
}
