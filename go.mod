module github.com/openshift/ci-search

go 1.17

require (
	cloud.google.com/go/storage v1.12.0
	github.com/docker/go-units v0.4.0
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/golang/protobuf v1.4.2
	github.com/gorilla/mux v1.7.3
	github.com/jmoiron/sqlx v1.3.1
	github.com/pkg/profile v1.5.0
	github.com/prometheus/client_golang v0.9.3
	github.com/spf13/cobra v0.0.6
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	gonum.org/v1/plot v0.8.1
	google.golang.org/api v0.32.0
	k8s.io/apimachinery v0.18.0
	k8s.io/client-go v0.18.0
	k8s.io/klog v1.0.0
	k8s.io/utils v0.0.0-20200324210504-a9aa75ae1b89
	modernc.org/sqlite v1.10.0
	vbom.ml/util v0.0.0-20180919145318-efcd4e0f9787
)

require (
	cloud.google.com/go v0.66.0 // indirect
	github.com/ajstarks/svgo v0.0.0-20180226025133-644b8db467af // indirect
	github.com/beorn7/perks v1.0.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fogleman/gg v1.3.0 // indirect
	github.com/go-latex/latex v0.0.0-20200518072620-0806b477ea35 // indirect
	github.com/gogo/protobuf v1.3.1 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/golang/groupcache v0.0.0-20200121045136-8c9f03a8e57e // indirect
	github.com/google/go-cmp v0.5.3 // indirect
	github.com/google/gofuzz v1.1.0 // indirect
	github.com/googleapis/gax-go/v2 v2.0.5 // indirect
	github.com/hashicorp/golang-lru v0.5.1 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/json-iterator/go v1.1.8 // indirect
	github.com/jstemmer/go-junit-report v0.9.1 // indirect
	github.com/jung-kurt/gofpdf v1.16.2 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/prometheus/client_model v0.0.0-20190812154241-14fe0d1b01d4 // indirect
	github.com/prometheus/common v0.4.0 // indirect
	github.com/prometheus/procfs v0.0.0-20190507164030-5867b95ac084 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20200410134404-eec4a21b6bb0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/xlab/handysort v0.0.0-20150421192137-fb3537ed64a1 // indirect
	go.opencensus.io v0.22.4 // indirect
	golang.org/x/crypto v0.0.0-20200622213623-75b288015ac9 // indirect
	golang.org/x/image v0.0.0-20200618115811-c13761719519 // indirect
	golang.org/x/lint v0.0.0-20200302205851-738671d3881b // indirect
	golang.org/x/mod v0.4.2 // indirect
	golang.org/x/net v0.0.0-20201021035429-f5854403a974 // indirect
	golang.org/x/oauth2 v0.0.0-20200902213428-5d25da1a8d43 // indirect
	golang.org/x/sys v0.0.0-20210320140829-1e4c9ba3b0c4 // indirect
	golang.org/x/text v0.3.3 // indirect
	golang.org/x/tools v0.1.0 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/appengine v1.6.6 // indirect
	google.golang.org/genproto v0.0.0-20200921151605-7abf4a1a14d5 // indirect
	google.golang.org/grpc v1.32.0 // indirect
	google.golang.org/protobuf v1.25.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
	k8s.io/api v0.18.0 // indirect
	modernc.org/cc/v3 v3.32.1 // indirect
	modernc.org/ccgo/v3 v3.9.1 // indirect
	modernc.org/libc v1.8.1 // indirect
	modernc.org/mathutil v1.2.2 // indirect
	modernc.org/memory v1.0.4 // indirect
	modernc.org/opt v0.1.1 // indirect
	modernc.org/strutil v1.1.1 // indirect
	modernc.org/token v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v3 v3.0.0 // indirect
	sigs.k8s.io/yaml v1.2.0 // indirect
)

replace k8s.io/client-go => k8s.io/client-go v0.17.4
