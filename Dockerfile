FROM openshift/origin-release:golang-1.11
WORKDIR /go/src/github.com/openshift/ci-search
COPY . .
RUN make build

FROM centos:7
COPY --from=0 /go/src/github.com/openshift/ci-search/search /usr/bin/
COPY --from=0 /go/src/github.com/openshift/ci-search/build-indexer /usr/bin/
RUN curl -L https://github.com/BurntSushi/ripgrep/releases/download/0.10.0/ripgrep-0.10.0-x86_64-unknown-linux-musl.tar.gz | \
    tar xvzf - --wildcards --no-same-owner --strip-components=1  -C /usr/bin '*/rg'
RUN mkdir /var/lib/ci-search && chown 1000:1000 /var/lib/ci-search && chmod 1777 /var/lib/ci-search
USER 1000:1000
ENTRYPOINT ["search"]
CMD ["--interval=5m", "--path=/var/lib/ci-search/"]
EXPOSE 8080
