export K53_IAM_ROLE_ARN=arn:aws:iam::${AWS_ACCOUNT_ID}:role/${CLUSTER_NAME}-k53
export KO_DOCKER_REPO=${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/k53

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.24.2

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=chart/k53/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: verify
verify: 
	go fmt ./...
	go vet ./...
	helm lint --strict chart/k53	

.PHONY: test
test: manifests generate verify envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

.PHONY: run
run: manifests generate verify ## Run a controller from your host.
	go run ./main.go

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest

CONTROLLER_TOOLS_VERSION ?= v0.9.2

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: build
build: ## Build the controller image
	$(eval CONTROLLER_TAG=$(shell KO_DOCKER_REPO=$(KO_DOCKER_REPO) $(WITH_GOFLAGS) ko build -B github.com/bwagner5/k53/cmd/controller | sed 's/.*controller@//'))
	echo Built ${CONTROLLER_TAG}

.PHONY: apply
apply: build ## Deploy the controller from the current state of your git repository into your ~/.kube/config cluster
	helm upgrade --install k53 chart/k53 --namespace k53 --create-namespace \
	$(HELM_OPTS) \
	--set serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=${K53_IAM_ROLE_ARN} \
	--set image.repository=$(KO_DOCKER_REPO)/controller \
	--set image.digest="$(CONTROLLER_TAG)"

.PHONY: help
help: ## Display help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
