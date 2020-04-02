module github.com/openshift/ci-search

go 1.13

require (
	cloud.google.com/go v0.55.0 // indirect
	cloud.google.com/go/storage v1.6.0
	github.com/docker/docker-credential-helpers v0.6.3 // indirect
	github.com/docker/go-units v0.4.0
	github.com/golang/protobuf v1.3.5
	github.com/gorilla/mux v1.7.3
	github.com/kelseyhightower/envconfig v1.4.0 // indirect
	github.com/openshift/library-go v0.0.0-20200402123743-4015ba624cae
	github.com/spf13/cobra v0.0.6
	golang.org/x/build v0.0.0-20190314133821-5284462c4bec // indirect
	golang.org/x/exp v0.0.0-20200320212757-167ffe94c325 // indirect
	golang.org/x/net v0.0.0-20200324143707-d3edc9973b7e // indirect
	golang.org/x/sys v0.0.0-20200327173247-9dae0f8f5775 // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	golang.org/x/tools v0.0.0-20200329025819-fd4102a86c65 // indirect
	google.golang.org/api v0.20.0
	google.golang.org/genproto v0.0.0-20200326112834-f447254575fd // indirect
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v0.18.0
	k8s.io/klog v1.0.0
	k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89
	knative.dev/pkg v0.0.0-20200325183451-8a6d25b30970 // indirect
	vbom.ml/util v0.0.0-20180919145318-efcd4e0f9787
)

replace k8s.io/client-go => k8s.io/client-go v0.17.4
