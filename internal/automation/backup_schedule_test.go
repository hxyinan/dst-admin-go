package automation

import (
	"errors"
	"sync"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestBackupScheduleEnableEditPausePreservesOnePlan(t *testing.T) {
	s, _, _ := newAutomationTestService(t, &automationTestExecutor{})
	first, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !first.EffectiveEnabled || first.NextRunAt == nil || first.Task.Schedule != "0 4 * * *" || first.Task.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected created schedule: %+v", first)
	}
	second, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(true)}, nil)
	if err != nil || second.Created || second.Task.ID != first.Task.ID {
		t.Fatal(second, err)
	}
	changed, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Schedule: ptr("0 */2 * * *"), Keep: ptr(12)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Task.ID != first.Task.ID || changed.Task.TimeoutSeconds != 600 || changed.Task.Timezone != first.Task.Timezone || changed.Task.Parameters["keep"] != float64(12) {
		t.Fatalf("partial update lost settings: %+v", changed.Task)
	}
	paused, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(false)}, nil)
	if err != nil || paused.EffectiveEnabled || paused.NextRunAt != nil {
		t.Fatal(paused, err)
	}
	tasks, _ := s.Tasks("room")
	if len(tasks) != 1 {
		t.Fatal("duplicate plans", tasks)
	}
	if _, err = s.ConfigureBackupSchedule("room", BackupScheduleInput{ExpectedRevision: first.Task.Revision, Enabled: ptr(true)}, nil); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale revision accepted", err)
	}
}

func TestBackupScheduleDoesNotEnableOtherTasksInPausedGroup(t *testing.T) {
	s, _, _ := newAutomationTestService(t, &automationTestExecutor{})
	first, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := s.Groups("room")
	g := groups[0]
	other, err := s.CreateTask("room", TaskInput{GroupID: g.ID, Name: "other task", Enabled: true, Action: ActionRoomRestart, Schedule: "0 0 * * *", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateGroup("room", g.ID, GroupInput{Name: g.Name, Type: g.Type, Enabled: false, ExpectedRevision: g.Revision}); err != nil {
		t.Fatal(err)
	}
	result, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{TaskID: first.Task.ID, Enabled: ptr(true)}, nil)
	if err != nil || !result.EffectiveEnabled {
		t.Fatal(result, err)
	}
	if result.Task.GroupID == g.ID {
		t.Fatal("backup stayed in paused group")
	}
	unchanged, _ := s.Task("room", other.ID)
	group, _ := s.store.Group("room", g.ID)
	if group.Enabled || unchanged.GroupID != g.ID {
		t.Fatal("unrelated paused task was enabled")
	}
}

func TestBackupScheduleFailureRollsBackAndAmbiguityIsExplicit(t *testing.T) {
	s, _, _ := newAutomationTestService(t, &automationTestExecutor{})
	if _, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Schedule: ptr("invalid")}, nil); err == nil {
		t.Fatal("invalid schedule accepted")
	}
	groups, _ := s.Groups("room")
	tasks, _ := s.Tasks("room")
	if len(groups) != 0 || len(tasks) != 0 {
		t.Fatal("failed write left orphan data")
	}
	for _, name := range []string{"morning", "evening"} {
		if _, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{CreateNew: true, Name: &name}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(true)}, nil); err == nil {
		t.Fatal("ambiguous plans silently changed")
	}
}

func TestConcurrentBackupEnableCreatesOnlyOnePlan(t *testing.T) {
	s, _, _ := newAutomationTestService(t, &automationTestExecutor{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ConfigureBackupSchedule("room", BackupScheduleInput{Enabled: ptr(true)}, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	tasks, _ := s.Tasks("room")
	if len(tasks) != 1 {
		t.Fatalf("concurrent enable created %d plans", len(tasks))
	}
}
