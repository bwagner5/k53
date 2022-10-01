/*
Copyright 2022.

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

package resolver

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/smithy-go/ptr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	srcv1 "github.com/bwagner5/k53/pkg/api/v1"
)

// ResolverReconciler reconciles a DNS object
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var dns srcv1.Resolver
	if err := r.Get(ctx, req.NamespacedName, &dns); err != nil {
		log.Error(err, "unable to fetch Resolver")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	dns.Status.State = ptr.String("Synchronized")
	if err := r.Status().Update(ctx, &dns); err != nil {
		log.Error(err, "unable to update Resolver status")
		return ctrl.Result{}, err
	}

	log.Info(fmt.Sprintf(`Reconciled "%s"`, dns.Spec.QueryLogConfig))
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&srcv1.Resolver{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
