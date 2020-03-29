FROM registry.svc.ci.openshift.org/openshift/release:golang-1.13
WORKDIR /go/src/github.com/openshift/ci-search
COPY . .
RUN make build

FROM centos:7
COPY --from=0 /go/src/github.com/openshift/ci-search/search /usr/bin/
RUN curl -L https://github.com/BurntSushi/ripgrep/releases/download/0.10.0/ripgrep-0.10.0-x86_64-unknown-linux-musl.tar.gz | \
    tar xvzf - --wildcards --no-same-owner --strip-components=1  -C /usr/bin '*/rg'
RUN mkdir /var/lib/ci-search && chown 1000:1000 /var/lib/ci-search && chmod 1777 /var/lib/ci-search
USER 1000:1000
ENTRYPOINT ["search"]
CMD ["--path=/var/lib/ci-search/", "--deck-uri=https://prow.svc.ci.openshift.org"]
EXPOSE 8080
