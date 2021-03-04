module github.com/openshift/ci-search

go 1.13

require (
	cloud.google.com/go/storage v1.12.0
	github.com/docker/go-units v0.4.0
	github.com/golang/protobuf v1.4.2
	github.com/gorilla/mux v1.7.3
	github.com/jmoiron/sqlx v1.3.1
	github.com/prometheus/client_golang v0.9.3
	github.com/spf13/cobra v0.0.6
	github.com/xlab/handysort v0.0.0-20150421192137-fb3537ed64a1 // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	gonum.org/v1/plot v0.8.1
	google.golang.org/api v0.32.0
	k8s.io/api v0.18.0 // indirect
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v0.18.0
	k8s.io/klog v1.0.0
	k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89
	modernc.org/sqlite v1.8.8
	vbom.ml/util v0.0.0-20180919145318-efcd4e0f9787
)

replace k8s.io/client-go => k8s.io/client-go v0.17.4
