FROM golang:1.10

WORKDIR /go/src/api
COPY . .
RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
RUN make dep
RUN make api
ENTRYPOINT ["bin/bc_api", "-c", "bin/config_api.json"]
