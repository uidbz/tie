FROM golang as builder
RUN go get -d -v github.com/julienschmidt/httprouter
RUN go get -d -v git.sr.ht/~uid/tiedb
RUN go get -d -v github.com/dhowden/tag
RUN go get -d -v github.com/h2non/filetype
RUN mkdir /app
ADD *.go /app/
WORKDIR /app

RUN CGO_ENABLED=0 GOOS=linux go build -a -o tie-daemon .
#CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o tie-daemon .

FROM alpine:latest
#RUN apk --no-cache add ca-certificates
COPY --from=builder  /app .
ENTRYPOINT ["./tie-daemon"]
