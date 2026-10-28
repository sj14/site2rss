FROM --platform=$BUILDPLATFORM golang:1 AS build
ARG TARGETOS TARGETARCH

WORKDIR /go/src/app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /go/bin/app

FROM gcr.io/distroless/static-debian13

COPY --from=build /go/bin/app /
CMD ["/app"]
