FROM gosaic-build:latest AS build

ADD . /src



WORKDIR /src

RUN go build -mod=vendor ./cmd/gosaic && go build -mod=vendor ./cmd/redisimport
RUN cp -av gosaic redisimport /usr/local/bin
RUN ldconfig

