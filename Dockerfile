FROM gosaic-build:latest AS build

ADD . /src

WORKDIR /src

RUN go build -mod=vendor ./cmd/gosaic && go build -mod=vendor ./cmd/redisimport
RUN cp -av gosaic redisimport /usr/local/bin
RUN ldconfig


FROM debian:buster-slim AS base


RUN apt-get -y update && apt-get -y install libglib2.0 libexpat1 libjpeg62-turbo libfftw3-3 libpng16-16 # libgirepository1.0

COPY --from=gosaic-build:latest /usr/local/lib/libvips.so.42.13.0 /usr/local/lib
RUN ln -s /usr/local/lib/libvips.so.42.13.0 /usr/local/lib/libvips.so.42 && ldconfig

COPY --from=build /usr/local/bin/gosaic /usr/local/bin/
COPY --from=build /usr/local/bin/redisimport /usr/local/bin/

RUN apt-get clean
