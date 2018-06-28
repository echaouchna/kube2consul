# Build binary stage
FROM golang
WORKDIR /go/src/github.com/echaouchna/
ENV CGO_ENABLED=0 GOOS=linux
RUN git clone https://github.com/echaouchna/kube2consul.git \
    && cd kube2consul \
    && make build

# Build image stage
FROM alpine:latest
COPY --from=0 /go/src/github.com/echaouchna/kube2consul/bin/kube2consul /bin/
RUN chmod u+x /bin/kube2consul
ENTRYPOINT ["kube2consul"]
