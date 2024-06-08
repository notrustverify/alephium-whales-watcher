# syntax=docker/dockerfile:1

# Dockerfile according https://docs.docker.com/language/golang/build-images/

FROM golang:1.22-bookworm AS build-stage
# Set destination for COPY
WORKDIR /app

#RUN apt-get update \
# && DEBIAN_FRONTEND=noninteractive \
#    apt-get install --no-install-recommends --assume-yes \
#      build-essential
# Download Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code. Note the slash at the end, as explained in
# https://docs.docker.com/engine/reference/builder/#copy
COPY *.go ./

# Build
RUN CGO_ENABLED=1 GOOS=linux go build -o out
# Run the tests in the container
#FROM build-stage AS run-test-stage
#RUN go test -v ./...

# Deploy the application binary into a lean image
FROM gcr.io/distroless/base-debian12
WORKDIR /
COPY --from=build-stage /app/out /out
CMD ["/out"]
