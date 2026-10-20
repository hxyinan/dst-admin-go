package httpapi

import (
	"net/http"

	"dont/internal/automation"
	"github.com/gin-gonic/gin"
)

type BackupScheduleService interface {
	Groups(string) ([]automation.Group, error)
	Tasks(string) ([]automation.Task, error)
	ConfigureBackupSchedule(string, automation.BackupScheduleInput, automation.BackupPolicyManager) (automation.BackupScheduleResult, error)
}

type BackupScheduleHandler struct {
	service   BackupScheduleService
	policy    automation.BackupPolicyManager
	scheduler AutomationScheduler
}

func NewBackupScheduleHandler(service BackupScheduleService, policy automation.BackupPolicyManager, scheduler AutomationScheduler) *BackupScheduleHandler {
	return &BackupScheduleHandler{service: service, policy: policy, scheduler: scheduler}
}
func (h *BackupScheduleHandler) Register(v2 *gin.RouterGroup) {
	v2.GET("/rooms/:roomId/backup-schedules", h.list)
	v2.POST("/rooms/:roomId/backup-schedules/actions/configure", h.configure)
}
func (h *BackupScheduleHandler) list(c *gin.Context) {
	room := c.Param("roomId")
	tasks, err := h.service.Tasks(room)
	if err != nil {
		automationFailure(c, err)
		return
	}
	groups, err := h.service.Groups(room)
	if err != nil {
		automationFailure(c, err)
		return
	}
	policy, err := h.policy.Policy(room)
	if err != nil {
		backupFailure(c, err)
		return
	}
	items := []automation.Task{}
	for _, task := range tasks {
		if task.Action == automation.ActionBackupCreate {
			items = append(items, task)
		}
	}
	Success(c, 200, gin.H{"items": items, "groups": groups, "legacyPolicy": policy, "defaults": automation.DefaultBackupSchedule()})
}
func (h *BackupScheduleHandler) configure(c *gin.Context) {
	var in automation.BackupScheduleInput
	if !bindAutomationJSON(c, &in) {
		return
	}
	result, err := h.service.ConfigureBackupSchedule(c.Param("roomId"), in, h.policy)
	if err != nil {
		automationFailure(c, err)
		return
	}
	if h.scheduler != nil {
		if err := h.scheduler.Reload(); err != nil {
			Failure(c, http.StatusServiceUnavailable, "BACKUP_SCHEDULE_RELOAD_FAILED", "备份计划已保存，但调度加载失败，请重新检查", gin.H{"saved": result})
			return
		}
	}
	Success(c, 200, result)
}
