package aicontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// A model outage must not erase a real platform result or suggest retrying a
// mutation. Read back any submitted jobs once, without resubmitting them.
func (s *Service) providerFallback(ctx context.Context, principal Principal, out ChatOutput, failure error, locale string, emit func(ChatEvent)) ChatOutput {
	jobs, order := returnedJobs(out.Calls)
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for index, id := range order {
		job := jobs[id]
		status, _ := job["status"].(string)
		room, _ := job["roomId"].(string)
		if index >= 4 || readCtx.Err() != nil || room == "" || status != "queued" && status != "running" {
			continue
		}
		args, _ := json.Marshal(Arguments{RoomID: room, JobID: id})
		result, err := s.Invoke(readCtx, principal, "get_job", args)
		if err == nil {
			out.Calls = append(out.Calls, result)
			emit(ChatEvent{Type: "result", Tool: "get_job", Call: &result})
		}
	}
	jobs, order = returnedJobs(out.Calls)
	code := "AI_PROVIDER_INVALID_RESPONSE"
	if failure != nil {
		code = sanitizeText(failure.Error())
	}
	if locale == "en-US" {
		out.Content = "The model could not continue replying (" + code + "). Submitted operations will not be repeated automatically."
	} else {
		out.Content = "模型暂时未能继续回复（" + code + "）。已提交的操作不会自动重发。"
	}
	if code == "AI_PROVIDER_QUOTA_EXHAUSTED" {
		if locale == "en-US" {
			out.Content = "The model API key has insufficient quota. Add available quota to that key or choose another model before continuing."
		} else {
			out.Content = "当前模型密钥额度不足，无法继续生成回复。请补充该密钥的可用额度，或切换模型后再试。"
		}
	}
	known, mutated := true, false
	for _, result := range out.Calls {
		call, err := s.Store.call(result.CallID)
		if err != nil {
			known = false
			continue
		}
		tool, ok := s.tool(call.Tool)
		known = known && ok
		mutated = mutated || tool.Mutation
	}
	if known && !mutated {
		if locale == "en-US" {
			out.Content += "\n\nNo management modification was executed in this turn."
		} else {
			out.Content += "\n\n本轮尚未执行管理修改。"
		}
	}
	for _, id := range order {
		job := jobs[id]
		status, _ := job["status"].(string)
		message := ""
		if locale == "en-US" {
			switch status {
			case "queued", "running":
				message = "Submitted; current state: " + status + ". Completion is not yet verified."
			case "succeeded":
				message = "Completed; outcome: " + fmt.Sprint(job["outcome"])
			default:
				message = "Current state: " + status
			}
		} else {
			switch status {
			case "queued":
				message = "已提交，当前正在排队，尚未确认完成。"
			case "running":
				message = "正在执行，尚未确认完成。"
			case "succeeded":
				message = "已完成，实际结果：" + fmt.Sprint(job["outcome"])
			case "failed":
				message = "执行失败。"
			case "cancelled", "canceled":
				message = "已取消。"
			default:
				message = "实际状态：" + status
			}
		}
		if detail, ok := job["errorMessage"].(string); ok && detail != "" {
			message += " " + detail
		}
		out.Content += "\n\n" + id + "：" + sanitizeText(message)
	}
	if len(order) == 0 {
		if locale == "en-US" {
			out.Content += fmt.Sprintf("\n\n%d platform results are retained in the processing details. Do not repeat a modification solely because the model reply failed; check its actual result first.", len(out.Calls))
		} else {
			out.Content += fmt.Sprintf("\n\n已保留 %d 条平台回执，可展开处理过程查看。请先核对实际结果，避免因模型回复中断而重复提交修改。", len(out.Calls))
		}
	}
	return out
}

func returnedJobs(calls []Invocation) (map[string]map[string]any, []string) {
	jobs := map[string]map[string]any{}
	order := []string{}
	for _, call := range calls {
		if call.Status != "succeeded" {
			continue
		}
		var value map[string]any
		if json.Unmarshal(call.Data, &value) != nil {
			continue
		}
		for depth := 0; value != nil && depth < 4; depth++ {
			id, _ := value["id"].(string)
			kind, _ := value["kind"].(string)
			status, _ := value["status"].(string)
			outcome, _ := value["outcome"].(string)
			if id != "" && kind != "" && status != "" && outcome != "" && !strings.ContainsAny(id, "\r\n") {
				if _, exists := jobs[id]; !exists {
					order = append(order, id)
				}
				jobs[id] = value
				break
			}
			next, _ := value["data"].(map[string]any)
			if next == nil {
				next, _ = value["job"].(map[string]any)
			}
			value = next
		}
	}
	return jobs, order
}
