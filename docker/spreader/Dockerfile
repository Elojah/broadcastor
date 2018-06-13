FROM golang:1.10

WORKDIR /go/src/spreader
COPY . .
RUN curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh
RUN make dep
RUN make spreader
ENTRYPOINT ["bin/bc_spreader", "-c", "bin/config_spreader.json"]
