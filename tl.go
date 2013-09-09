package main

import "bufio"
import "crypto/sha1"
import "errors"
import "fmt"
import "html/template"
import "io"
import "log"
import "os"
import "strconv"
import "strings"
import "time"

type Event struct {
	Name             string
	Time             time.Time
	OriginalDuration time.Duration
	Duration         time.Duration
}

type Day struct {
	Events []Event
}

type Report struct {
	Days []Day
}

func read_data_file(in io.Reader) (events []Event, err error) {
	lines := bufio.NewScanner(in)
	line_number := 0
	for lines.Scan() {
		line_number++
		fields := strings.SplitN(lines.Text(), " ", 7)
		var numerically [6]int
		for i := 0; i < 6; i++ {
			var err error
			numerically[i], err = strconv.Atoi(fields[i])
			if err != nil {
				return nil, errors.New(fmt.Sprint("Field ", i, " on line ", line_number, " is not numeric: ", err))
			}
		}
		events = append(events, Event{
			Name: fields[6],
			Time: time.Date(
				numerically[0],
				time.Month(numerically[1]),
				numerically[2],
				numerically[3],
				numerically[4],
				numerically[5],
				0, // Nanoseconds
				time.Local),
		})
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return
}

func calculate_durations(events []Event) {
	for i := range events[:len(events)-1] {
		d := events[i+1].Time.Sub(events[i].Time)
		events[i].OriginalDuration = d
		events[i].Duration = d
	}
	d := time.Now().Sub(events[len(events)-1].Time)
	events[len(events)-1].OriginalDuration = d
	events[len(events)-1].Duration = d
}

func start_of_day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func start_of_next_day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.Local)
}

func split_by_day(events []Event) (by_day []Day) {
	var current_day time.Time
	var this_day Day
	for _, e := range events {
		for {
			first_day_of_e := e
			day := start_of_day(e.Time)
			if start_of_day(e.Time.Add(e.Duration)) != day {
				split_at := start_of_next_day(e.Time)
				first_day_of_e.Duration = split_at.Sub(e.Time)
				e.Time = split_at
				e.Duration -= first_day_of_e.Duration

			}
			if current_day != day {
				if !current_day.IsZero() {
					by_day = append(by_day, this_day)
					this_day = Day{}
				}
				current_day = day
			}
			this_day.Events = append(this_day.Events, first_day_of_e)
			if start_of_day(first_day_of_e.Time) == start_of_day(e.Time) {
				break
			}
		}
	}
	by_day = append(by_day, this_day)
	return
}

func (e *Event) TimeOfDay() int {
	return e.Time.Hour()*3600 + e.Time.Minute()*60 + e.Time.Second()
}

func (e *Event) Color() template.CSS {
	if e.Name == "" {
		return template.CSS("white")
	}
	hash := sha1.New()
	io.WriteString(hash, e.Name)
	hue := 360.0 * int(hash.Sum(nil)[0]) / 256.0
	return template.CSS("hsl(" + strconv.Itoa(hue) + ",90%,45%)")
}

func (e *Event) DurationDescription() string {
	if e.OriginalDuration.Hours() > 24 {
		return fmt.Sprintf("%.1f days", e.OriginalDuration.Hours()/24)
	}
	if e.OriginalDuration.Hours() > 1 {
		return fmt.Sprintf("%.1f hours", e.OriginalDuration.Hours())
	}
	if e.OriginalDuration.Minutes() > 1 {
		return fmt.Sprintf("%.1f min", e.OriginalDuration.Minutes())
	}
	return fmt.Sprintf("%.0f sec", e.OriginalDuration.Seconds())
}

func (e *Event) Height() float32 {
	return 100 * float32(e.Duration.Seconds()) / 86400
}

func (r *Report) DayWidth() float32 {
	return 100.0 / float32(len(r.Days))
}

func generate_report(days []Day) (td Report) {
	td.Days = days
	return
}

func backfill_first_day(d *Day) {
	// Stuff an empty event at the beginning of the first day
	first_event_time := d.Events[0].Time
	start_of_first_day := start_of_day(first_event_time)
	time_until_first_event := first_event_time.Sub(start_of_first_day)
	first_day_events := append([]Event{Event{Duration: time_until_first_event}}, d.Events...)
	d.Events = first_day_events
}

func execute_template(r Report) error {
	t := template.New("tl")
	t, err := t.ParseFiles("tl.template")
	if err != nil {
		return err
	}
	err = t.ExecuteTemplate(os.Stdout, "tl.template", &r)
	if err != nil {
		return err
	}
	return nil
}

func view_handler(in io.Reader) error {
	all_events, err := read_data_file(in)
	if err != nil {
		return err
	}
	calculate_durations(all_events)
	by_day := split_by_day(all_events)
	backfill_first_day(&by_day[0])
	report := generate_report(by_day)
	err = execute_template(report)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	if err := view_handler(os.Stdin); err != nil {
		log.Fatalln(err)
	}
}
