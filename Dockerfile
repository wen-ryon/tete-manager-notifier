FROM golang:1.24-alpine AS builder
RUN go env -w GOPROXY=https://goproxy.cn,direct
WORKDIR /app
# 先拷贝 mod 文件利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /tete-manager-notifier ./cmd/app
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
# 设置时区为上海
ENV TZ=Asia/Shanghai
WORKDIR /root/
# 从编译阶段拷贝二进制文件
COPY --from=builder /tete-manager-notifier .
CMD ["./tete-manager-notifier"]