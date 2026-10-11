package models

import "testing"

func valid() Routine {
	return Routine{AccountID: "a", Name: "n", GmailQuery: "label:x", ActionType: ActionArchive,
		ScheduleType: ScheduleDaily, ScheduleConfig: ScheduleConfig{Time: "03:00"}, MaxMessagesPerRun: 10}
}

func TestRoutineValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	mut := map[string]func(*Routine){
		"name":                 func(r *Routine) { r.Name = " " },
		"gmail_query":          func(r *Routine) { r.GmailQuery = "" },
		"action_type":          func(r *Routine) { r.ActionType = "delete_forever" },
		"action_config.label":  func(r *Routine) { r.ActionType = ActionAddLabel; r.ActionConfig.Label = "trash" },
		"max_messages_per_run": func(r *Routine) { r.MaxMessagesPerRun = 10001 },
		"schedule_type":        func(r *Routine) { r.ScheduleType = "yearly" },
		"account_id":           func(r *Routine) { r.AccountID = "" },
	}
	for field, f := range mut {
		r := valid()
		f(&r)
		err := r.Validate()
		ve, ok := err.(ValidationErrors)
		if !ok || ve[field] == "" {
			t.Errorf("%s: got %v", field, err)
		}
	}
}

func TestDestructive(t *testing.T) {
	if !ActionTrash.Destructive() || ActionArchive.Destructive() {
		t.Fatal("only trash is destructive")
	}
	for _, a := range ActionTypes {
		if !a.Valid() {
			t.Fatal(a)
		}
	}
}
