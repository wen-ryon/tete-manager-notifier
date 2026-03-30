package notifier

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
)

type Payload struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// SendNotification 发送统一通知
func SendNotification(apiEndpoint, title, content string) error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("推送通知 panic: %v\n%s", r, string(debug.Stack()))
		}
	}()
	payload := Payload{
		Title:   title,
		Content: content,
	}

	data, _ := json.Marshal(payload)
	resp, err := http.Post(apiEndpoint, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("推送返回状态码: %d", resp.StatusCode)
	}
	return nil
}
