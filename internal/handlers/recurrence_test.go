package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

func strPtr(s string) *string { return &s }

func intPtr(n int) *int { return &n }

func TestApplyRecurrenceLifecycle(t *testing.T) {
	const today = "2026-01-05"

	t.Run("non-recurring item never gets a next due date", func(t *testing.T) {
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-01")}
		applyRecurrenceLifecycle(item, &models.Item{}, today)
		if !item.Done || item.NextDueDate != nil {
			t.Fatalf("expected a plain done item, got done=%v next=%v", item.Done, item.NextDueDate)
		}
	})

	t.Run("checking off a recurring item keeps it done and schedules the next occurrence", func(t *testing.T) {
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY")}
		applyRecurrenceLifecycle(item, &models.Item{DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY")}, today)
		if !item.Done {
			t.Fatal("expected the item to stay done")
		}
		if item.NextDueDate == nil || *item.NextDueDate != "2026-01-12" {
			t.Fatalf("expected next due date 2026-01-12, got %v", item.NextDueDate)
		}
		if *item.DueDate != "2026-01-05" {
			t.Fatalf("expected due date to stay on the completed occurrence, got %q", *item.DueDate)
		}
	})

	t.Run("un-checking cancels the pending occurrence", func(t *testing.T) {
		item := &models.Item{Done: false, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY")}
		before := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY"), NextDueDate: strPtr("2026-01-12")}
		applyRecurrenceLifecycle(item, before, today)
		if item.NextDueDate != nil {
			t.Fatalf("expected no next due date on an active item, got %q", *item.NextDueDate)
		}
	})

	t.Run("an unrelated edit of a done item keeps its next occurrence", func(t *testing.T) {
		before := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY"), NextDueDate: strPtr("2026-01-12")}
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY")}
		// Much later "today": recomputing would skip ahead, keeping must not.
		applyRecurrenceLifecycle(item, before, "2026-01-11")
		if item.NextDueDate == nil || *item.NextDueDate != "2026-01-12" {
			t.Fatalf("expected next due date kept at 2026-01-12, got %v", item.NextDueDate)
		}
	})

	t.Run("changing the rule of a done item reschedules it", func(t *testing.T) {
		before := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY"), NextDueDate: strPtr("2026-01-12")}
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("DAILY")}
		applyRecurrenceLifecycle(item, before, today)
		if item.NextDueDate == nil || *item.NextDueDate != "2026-01-06" {
			t.Fatalf("expected next due date 2026-01-06, got %v", item.NextDueDate)
		}
	})

	t.Run("an ended series stays done with no next occurrence", func(t *testing.T) {
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY"), RecurrenceEndDate: strPtr("2026-01-10")}
		applyRecurrenceLifecycle(item, &models.Item{}, today)
		if !item.Done || item.NextDueDate != nil {
			t.Fatalf("expected a finished series, got done=%v next=%v", item.Done, item.NextDueDate)
		}
	})

	t.Run("the end date itself is still an occurrence", func(t *testing.T) {
		item := &models.Item{Done: true, DueDate: strPtr("2026-01-05"), RecurrenceRule: strPtr("WEEKLY"), RecurrenceEndDate: strPtr("2026-01-12")}
		applyRecurrenceLifecycle(item, &models.Item{}, today)
		if item.NextDueDate == nil || *item.NextDueDate != "2026-01-12" {
			t.Fatalf("expected next due date 2026-01-12, got %v", item.NextDueDate)
		}
	})
}

func TestNextOccurrence(t *testing.T) {
	cases := []struct {
		name  string
		from  string
		rule  string
		today string
		want  string
	}{
		{"completed on its due date", "2026-01-05", "WEEKLY", "2026-01-05", "2026-01-12"},
		{"completed early advances from the due date", "2026-01-09", "WEEKLY", "2026-01-05", "2026-01-16"},
		{"overdue skips missed occurrences", "2025-12-01", "WEEKLY", "2026-01-05", "2026-01-12"},
		{"overdue daily lands on tomorrow", "2025-12-01", "DAILY", "2026-01-05", "2026-01-06"},
		{"no due date starts from today", "", "DAILY", "2026-01-05", "2026-01-06"},
		{"every x months", "2026-01-15", "EVERY_X_MONTHS:3", "2026-01-15", "2026-04-15"},
		// The spec's example: Mon/Wed/Fri, completed on Friday → Monday.
		{"weekdays rule completed on Friday", "2026-10-02", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-02", "2026-10-05"},
		{"weekdays rule overdue since Monday", "2026-09-28", "FREQ=WEEKLY;BYDAY=MO,WE,FR", "2026-10-01", "2026-10-02"},
		{"every 2 weeks on Tuesday, overdue", "2026-09-01", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU", "2026-10-01", "2026-10-13"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextOccurrence(tc.from, tc.rule, tc.today)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("nextOccurrence(%q, %q, %q) = %q, want %q", tc.from, tc.rule, tc.today, got, tc.want)
			}
		})
	}

	t.Run("rejects unrecognized rule", func(t *testing.T) {
		if _, err := nextOccurrence("2026-01-01", "FORTNIGHTLY", "2026-01-01"); err == nil {
			t.Fatal("expected an error for an unrecognized rule")
		}
	})
}

func TestReminderMoment(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	cases := []struct {
		name       string
		dueTime    string
		atDueTime  bool
		offsetDays int
		timeOfDay  string
		want       time.Time
	}{
		{"same day", "", false, 0, "09:00", time.Date(2026, 3, 10, 9, 0, 0, 0, paris)},
		{"day before", "", false, 1, "20:00", time.Date(2026, 3, 9, 20, 0, 0, 0, paris)},
		{"at the exact due time", "18:30", true, 0, "09:00", time.Date(2026, 3, 10, 18, 30, 0, 0, paris)},
		{"at due time falls back without a due time", "", true, 1, "20:00", time.Date(2026, 3, 9, 20, 0, 0, 0, paris)},
		{"a due time alone doesn't switch mode", "18:30", false, 0, "09:00", time.Date(2026, 3, 10, 9, 0, 0, 0, paris)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reminderMoment("2026-03-10", tc.dueTime, tc.atDueTime, tc.offsetDays, tc.timeOfDay, paris)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("reminderMoment = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOccurrenceStart(t *testing.T) {
	loc := time.UTC
	midnight := time.Date(2026, 1, 13, 0, 0, 0, 0, loc)

	t.Run("start of the due day without a reminder", func(t *testing.T) {
		got, err := occurrenceStart(&db.NextOccurrenceCandidate{NextDueDate: "2026-01-13"}, loc)
		if err != nil || !got.Equal(midnight) {
			t.Fatalf("occurrenceStart = %v, %v; want %v", got, err, midnight)
		}
	})

	t.Run("a day-before reminder brings it back earlier", func(t *testing.T) {
		c := &db.NextOccurrenceCandidate{NextDueDate: "2026-01-13", ReminderEnabled: true, OffsetDays: intPtr(1), TimeOfDay: strPtr("20:00")}
		want := time.Date(2026, 1, 12, 20, 0, 0, 0, loc)
		got, err := occurrenceStart(c, loc)
		if err != nil || !got.Equal(want) {
			t.Fatalf("occurrenceStart = %v, %v; want %v", got, err, want)
		}
	})

	t.Run("a same-day reminder doesn't delay it", func(t *testing.T) {
		c := &db.NextOccurrenceCandidate{NextDueDate: "2026-01-13", ReminderEnabled: true, OffsetDays: intPtr(0), TimeOfDay: strPtr("09:00")}
		got, err := occurrenceStart(c, loc)
		if err != nil || !got.Equal(midnight) {
			t.Fatalf("occurrenceStart = %v, %v; want %v", got, err, midnight)
		}
	})
}

func TestDescribeDueFR(t *testing.T) {
	now := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC) // a Friday
	cases := []struct {
		dueDate, dueTime, want string
	}{
		{"2026-10-09", "18:00", "Échéance aujourd'hui à 18:00"},
		{"2026-10-10", "", "Échéance demain"},
		{"2026-10-11", "", "Échéance le dim. 11 oct."},
		{"2027-01-04", "07:30", "Échéance le lun. 4 janv. 2027 à 07:30"},
		{"2026-10-05", "", "Échéance dépassée (lun. 5 oct.)"},
	}
	for _, tc := range cases {
		if got := describeDueFR(tc.dueDate, tc.dueTime, now); got != tc.want {
			t.Errorf("describeDueFR(%q, %q) = %q, want %q", tc.dueDate, tc.dueTime, got, tc.want)
		}
	}
}

func TestResolveReminderDefaults(t *testing.T) {
	user := &models.User{ReminderDefaultOffsetDays: 0, ReminderDefaultTime: "09:00", ReminderDefaultAtDueTime: true}

	atDueTime, offset, timeOfDay := resolveReminderDefaults(user, true, nil, nil, nil)
	if !atDueTime || offset == nil || *offset != 0 || timeOfDay == nil || *timeOfDay != "09:00" {
		t.Fatalf("full default = %v/%v/%v, want true/0/09:00", atDueTime, offset, timeOfDay)
	}

	// An explicit timing without a mode is an explicit offset reminder.
	atDueTime, offset, _ = resolveReminderDefaults(user, true, nil, intPtr(1), strPtr("20:00"))
	if atDueTime || *offset != 1 {
		t.Fatalf("explicit timing = %v/%v, want false/1", atDueTime, *offset)
	}

	// "At the due time" with nothing else takes the fallback timing from the
	// default.
	no := false
	yes := true
	user.ReminderDefaultAtDueTime = false
	user.ReminderDefaultOffsetDays = 1
	user.ReminderDefaultTime = "20:00"
	atDueTime, offset, timeOfDay = resolveReminderDefaults(user, true, &yes, nil, strPtr(""))
	if !atDueTime || *offset != 1 || *timeOfDay != "20:00" {
		t.Fatalf("at due time = %v/%v/%v, want true/1/20:00", atDueTime, *offset, *timeOfDay)
	}

	atDueTime, offset, timeOfDay = resolveReminderDefaults(user, false, &no, intPtr(3), strPtr("10:00"))
	if atDueTime || offset != nil || timeOfDay != nil {
		t.Fatal("expected a disabled reminder to resolve to nothing")
	}
}

// patchItem sends a PATCH /api/v1/items/{id} with body as owner.
func patchItem(t *testing.T, app *Application, owner *models.User, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	idStr := strconv.FormatInt(id, 10)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/items/"+idStr, strings.NewReader(body))
	req.SetPathValue("id", idStr)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec := httptest.NewRecorder()
	app.handleItemsPatch(rec, req)
	return rec
}

// TestRecurringTaskLifecycle drives a recurring task through the API the
// way the frontend does: checked off → done with a next_due_date → back as
// active on that date once RunNextOccurrenceScan sees it has started.
func TestRecurringTaskLifecycle(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	ctx := context.Background()

	today := app.today()
	rule := "DAILY"
	item, err := app.DB.CreateItem(ctx, list.ID, "Arroser les plantes", nil, 1, nil, false, 0, nil, &today, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	done := decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"done":true}`))
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if !done.Done || done.NextDueDate == nil || *done.NextDueDate != tomorrow || *done.DueDate != today {
		t.Fatalf("after check-off: done=%v due=%v next=%v; want done, due %s, next %s", done.Done, done.DueDate, done.NextDueDate, today, tomorrow)
	}

	// Tomorrow hasn't started: the scan leaves it in "Terminés".
	app.RunNextOccurrenceScan(ctx)
	still, err := app.DB.GetItem(ctx, item.ID)
	if err != nil || !still.Done {
		t.Fatalf("expected the item to stay done before its next occurrence, got %+v (err %v)", still, err)
	}

	// Un-checking by hand cancels the pending occurrence.
	undone := decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"done":false}`))
	if undone.Done || undone.NextDueDate != nil || *undone.DueDate != today {
		t.Fatalf("after un-check: done=%v due=%v next=%v", undone.Done, undone.DueDate, undone.NextDueDate)
	}

	// A next occurrence that has already started comes back on the scan.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"done":true}`))
	if _, err := app.DB.SetItemSchedule(ctx, item.ID, nil, &yesterday); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	app.RunNextOccurrenceScan(ctx)
	back, err := app.DB.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if back.Done || back.NextDueDate != nil || *back.DueDate != yesterday {
		t.Fatalf("after scan: done=%v due=%v next=%v; want active, due %s", back.Done, back.DueDate, back.NextDueDate, yesterday)
	}
}

// TestPatchDueTimeAndRecurrence covers the PATCH field rules around the
// schedule: clearing recurrence keeps the due date (any item may have
// one), a due time needs a due date, and clearing the date clears the time.
func TestPatchDueTimeAndRecurrence(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	ctx := context.Background()

	due := "2026-10-11"
	rule := "WEEKLY"
	item, err := app.DB.CreateItem(ctx, list.ID, "Réunion", nil, 1, nil, false, 0, nil, &due, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got := decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"recurrence_rule":"","due_date":"2026-10-11","due_time":"14:30"}`))
	if got.RecurrenceRule != nil || got.DueDate == nil || *got.DueDate != due || got.DueTime == nil || *got.DueTime != "14:30" {
		t.Fatalf("got rule=%v due=%v time=%v; want no rule, due %s at 14:30", got.RecurrenceRule, got.DueDate, got.DueTime, due)
	}

	if rec := patchItem(t, app, owner, item.ID, `{"due_date":"","due_time":"10:00"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a due time without a date, got %d", rec.Code)
	}
	if rec := patchItem(t, app, owner, item.ID, `{"due_time":"25:00"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid due time, got %d", rec.Code)
	}

	cleared := decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"due_date":""}`))
	if cleared.DueDate != nil || cleared.DueTime != nil {
		t.Fatalf("expected clearing the date to clear the time, got due=%v time=%v", cleared.DueDate, cleared.DueTime)
	}
}

// TestCreateItemAtDueTimeReminder covers handleItemsCreate with the new
// fields: a due time is stored, and reminder_at_due_time resolves from the
// caller's default when left unset.
func TestCreateItemAtDueTimeReminder(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	owner.ReminderDefaultAtDueTime = true

	body := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Dentiste","due_date":"2026-10-11","due_time":"16:15","reminder_enabled":true,"reminder_offset_days":null,"reminder_time":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec := httptest.NewRecorder()
	app.handleItemsCreate(rec, req)

	item := decodeItemResponse(t, rec)
	if item.DueTime == nil || *item.DueTime != "16:15" || !item.ReminderAtDueTime {
		t.Fatalf("got due_time=%v at_due_time=%v; want 16:15/true", item.DueTime, item.ReminderAtDueTime)
	}

	bad := `{"list_id":` + strconv.FormatInt(list.ID, 10) + `,"title":"Sans date","due_time":"16:15"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/items", strings.NewReader(bad))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, owner))
	rec = httptest.NewRecorder()
	app.handleItemsCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a due time without a date, got %d", rec.Code)
	}
}

// TestPatchNormalizesRecurrenceRule covers the API side of the RRULE
// storage: any accepted spelling is stored and echoed canonically, and a
// weekly-on-days task checked off on its Friday occurrence schedules Monday.
func TestPatchNormalizesRecurrenceRule(t *testing.T) {
	app := newTestApplication(t)
	owner, list := itemTestFixture(t, app)
	ctx := context.Background()

	friday := "2026-10-02"
	item, err := app.DB.CreateItem(ctx, list.ID, "Sport", nil, 1, nil, false, 0, nil, &friday, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got := decodeItemResponse(t, patchItem(t, app, owner, item.ID, `{"recurrence_rule":"rrule:freq=weekly;byday=fr,mo,we"}`))
	if got.RecurrenceRule == nil || *got.RecurrenceRule != "FREQ=WEEKLY;BYDAY=MO,WE,FR" {
		t.Fatalf("recurrence_rule = %v, want FREQ=WEEKLY;BYDAY=MO,WE,FR", got.RecurrenceRule)
	}

	if rec := patchItem(t, app, owner, item.ID, `{"recurrence_rule":"FREQ=MONTHLY;BYDAY=MO"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for BYDAY on a monthly rule, got %d", rec.Code)
	}

	// Scheduling itself is date-driven (see TestNextOccurrence); with the
	// Friday occurrence in the past, the next one is the first Mon/Wed/Fri
	// after today.
	before := &models.Item{DueDate: &friday, RecurrenceRule: got.RecurrenceRule}
	done := &models.Item{Done: true, DueDate: &friday, RecurrenceRule: got.RecurrenceRule}
	applyRecurrenceLifecycle(done, before, friday)
	if done.NextDueDate == nil || *done.NextDueDate != "2026-10-05" {
		t.Fatalf("next due date = %v, want 2026-10-05 (the Monday)", done.NextDueDate)
	}
}
