package main

import (
	"bufio"
	"crypto/sha1"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
        "math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var initial_days = flag.Int(
	"initial_days", 14,
	"How many days to display initially")

var listen_address = flag.String(
	"listen_address", "",
	"Local address to listen on.  Typically \"\" (permissive) or \"localhost\" (restrictive)")

var log_filename = flag.String(
	"log_file", "tl.log",
	"Where to keep the log")

var port = flag.Int(
	"port", 29804,
	"Port to listen on")

var template_path = flag.String(
	"template_path", ".",
	"Where to find the HTML template file")

type Event struct {
	Name             string
	Time             time.Time
	OriginalDuration time.Duration // Duration before day splitting
	Duration         time.Duration // Intra-day (display) duration
	TotalDuration    time.Duration // Sum of all similarly-named Events
}

type Day struct {
	Date   time.Time
	Events []Event
}

type Week struct {
        Number          int
        AverageWH       float32  // Average Working Hours
        AverageSH       float32  // Average hours matching search term
        Days            []Day
}

type Report struct {
	Weeks           []Week
        SearchTerm      string
        SearchDate      string
}

var ignoreNames = map[string]string {"screensaver":"", "":""}
var otherWork = "work"

func read_data_file(in io.Reader) (events []Event, err error) {
	// Reads data lines, merging consecutive lines with the same name.
	lines := bufio.NewScanner(in)
	line_number := 0
	last_name := ""
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
		if fields[6] == last_name {
			continue
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
		last_name = fields[6]
	}
	if err := lines.Err(); err != nil {
		return nil, err
	}
	return
}

func drop_consecutive_events(in []Event) (out []Event) {
        last_name := ""
        for _, e := range in {
                if e.Name == last_name {
                        continue
                }
                out = append(out, e)
                last_name = e.Name
        }
        return
}

func calculate_durations(events []Event) {
	// The duration of an event is the difference between that event's
	// timestamp and the following event's timestamp.  I.e., Event.Time
	// is the beginning of the event.
	for i := range events[:len(events)-1] {
		d := events[i+1].Time.Sub(events[i].Time)
		events[i].OriginalDuration = d
		events[i].Duration = d
	}
	d := time.Now().Sub(events[len(events)-1].Time)
	events[len(events)-1].OriginalDuration = d
	events[len(events)-1].Duration = d
}

func calculate_total_durations(events []Event) {
	totals := make(map[string]time.Duration)
	for _, e := range events {
		totals[e.Name] += e.Duration
	}
	for i, e := range events {
		events[i].TotalDuration = totals[e.Name]
	}
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
			if this_day.Date.IsZero() {
				this_day.Date = day
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

func group_by_week(by_day []Day) (by_week []Week) {
        var this_week Week
        for _, d := range by_day {
                _, week := d.Date.ISOWeek()
                if (week != this_week.Number) {
                        if (this_week.Number != 0) {
                                this_week.AverageWH /= float32(math.Min(5,
                                                        float64( len(this_week.Days))))
                                this_week.AverageSH /= float32(math.Min(5,
                                                        float64( len(this_week.Days))))
                                by_week = append(by_week, this_week)
                                this_week = Week{}
                        }
                        this_week.Number = week
                }
                this_week.AverageWH += float32(d.WorkingHours())
                this_week.AverageSH += float32(d.SearchHours())
                this_week.Days = append(this_week.Days, d)
        }
        this_week.AverageWH /= float32(math.Min(5, 
                                        float64(len(this_week.Days))))
        by_week = append(by_week, this_week)
        return
}

func (e *Event) TimeOfDay() int {
	return e.Time.Hour()*3600 + e.Time.Minute()*60 + e.Time.Second()
}

func (e *Event) Color() template.CSS {
	if e.Name == "" {
		return template.CSS("white")
	}
        if (e.Name == otherWork) {
                return template.CSS("grey")
        }
	hash := sha1.New()
	io.WriteString(hash, e.Name)
	hue := 360.0 * int(hash.Sum(nil)[0]) / 256.0
	return template.CSS("hsl(" + strconv.Itoa(hue) + ",90%,45%)")
}

func DescribeDuration(t time.Duration) string {
	if t.Hours() > 24 {
		return fmt.Sprintf("%.1f days", t.Hours()/24)
	}
	if t.Hours() > 1 {
		return fmt.Sprintf("%.1f hours", t.Hours())
	}
	if t.Minutes() > 1 {
		return fmt.Sprintf("%.1f min", t.Minutes())
	}
	return fmt.Sprintf("%.0f sec", t.Seconds())
}

func (e *Event) DurationDescription() string {
	if e.OriginalDuration == e.TotalDuration {
		return DescribeDuration(e.OriginalDuration)
	}
	return (DescribeDuration(e.OriginalDuration) +
		" of " + DescribeDuration(e.TotalDuration))

}

func (d *Day) WorkingHours() float64 {
	sum := 0.0
	for _, e := range d.Events {
		if _, ok := ignoreNames[e.Name]; ok {
			continue
		}
		sum += e.Duration.Hours()
	}
	return sum
}

func (d *Day) SearchHours() float64 {
        sum := 0.0
        for _, e := range d.Events {
                if _, ok := ignoreNames[e.Name]; ok {
                        continue
                }
                if e.Name != otherWork {
                        sum += e.Duration.Hours()
                }
        }
        return sum
}

func (w *Week) SearchHours() float64 {
        sum := 0.0
        for _, d := range w.Days {
                sum += d.SearchHours()
        }
        return sum
}

func (w *Week) WorkingHours() float64 {
        sum := 0.0
        for _, d := range w.Days {
                sum += d.WorkingHours()
        }
        return sum
}

func (d *Day) Description() string {
	return  d.Date.Format("2006-01-02") + " (" +
		fmt.Sprintf("%.1f", d.WorkingHours()) +
		" WH)"
}

func (d *Day) SearchDescription() string {
        return  d.Date.Format("2006-01-02") + " (" +
                fmt.Sprintf("%.1f/%.1f",
                 d.SearchHours(), d.WorkingHours()) +
                " WH)"
}

func (w *Week) Description() string {
        return fmt.Sprintf("Week %d (%.1f WH/day)",
                           w.Number, w.AverageWH)
}

func (w *Week) SearchDescription() string {
        sh := w.SearchHours()
        wh := w.WorkingHours()
        return fmt.Sprintf("Week %d (%.1f/%.1f WH - %.1f%%)",
                            w.Number, sh, wh, sh/wh * 100.0)
}

func (w *Week) NumDays() int {
        return len(w.Days)
}

func (w *Week) DayWidth() float32 {
        return 100 / float32(len(w.Days))
}

func (e *Event) Height() float32 {
	return 100 * float32(e.Duration.Seconds()) / 86400
}

func (r Report) NumDays() int {
        days := 0
        for _, w := range r.Weeks {
                days += len(w.Days)
        }
        return days
}

func (r Report) BodyWidth() float32 {
	days_on_screen := *initial_days
	if r.NumDays() < days_on_screen {
		days_on_screen = r.NumDays()
	}
	return 100 * float32(r.NumDays()) / float32(days_on_screen)
}

func (r Report) DayWidth() float32 {
	return 100.0 / float32(r.NumDays())
}

func (r Report) WorkingHours() string {
        sum := 0.0
        for _, w := range r.Weeks {
                sum += w.WorkingHours()
        }
        return fmt.Sprintf("%.1f", sum)
}

func (r Report) SearchHours() string {
        sum := 0.0
        for _, w := range r.Weeks {
                sum += w.SearchHours()
        }
        return fmt.Sprintf("%.1f", sum)
}

func (r Report) SearchPercent() string {
        sum_s := 0.0
        sum_w := 0.0
        for _, w := range r.Weeks {
                sum_s += w.SearchHours()
                sum_w += w.WorkingHours()
        }
        return fmt.Sprintf("%.1f", sum_s/sum_w * 100.0)
}



func generate_report(weeks []Week) (td Report) {
	td.Weeks = weeks
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

func sum(a int, b float32) float32 { return float32(a) * b }

func execute_template(template_name string, data interface{}, out io.Writer) error {
        funcMap := template.FuncMap{"sum": sum}
	t := template.New("tl").Funcs(funcMap)
	t, err := t.ParseFiles(filepath.Join(*template_path, template_name))
	if err != nil {
		return err
	}
	err = t.ExecuteTemplate(out, template_name, data)
	if err != nil {
		return err
	}
	return nil
}

func filter_by_term(events []Event, search_term string) {
        for i, e := range events {
                if new_name, ok := ignoreNames[e.Name]; ok {
                        events[i].Name = new_name
                        continue
                }
                if strings.Index(strings.ToLower(e.Name),
                                 strings.ToLower(search_term)) == -1 {
                        events[i].Name = otherWork
                }
        }
}
func filter_after_date(events []Event, start_date time.Time) (out []Event) {
        for _, e := range events {
                if start_date.After(e.Time) {
                        continue
                }
                out = append(out, e)
        }
        return
}

func view_handler(w http.ResponseWriter, r *http.Request) {
	log_file, err := os.Open(*log_filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer log_file.Close()
	all_events, err := read_data_file(log_file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	calculate_durations(all_events)
	calculate_total_durations(all_events)
	by_day := split_by_day(all_events)
	backfill_first_day(&by_day[0])
        by_week := group_by_week(by_day)
	report := generate_report(by_week)
	err = execute_template("view.template", report, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	return
}

func search_handler(w http.ResponseWriter, r *http.Request) {
        log_file, err := os.Open(*log_filename)
        if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
        }
        defer log_file.Close()
        all_events, err := read_data_file(log_file)
        if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
        }
        search_term := r.URL.Query().Get("q")
        filter_by_term(all_events, search_term)
        search_date := r.URL.Query().Get("date")
        if (search_date != "") {
                parts := strings.Split(search_date, "-")
                year,  _ := strconv.Atoi(parts[0])
                month,  _ := strconv.Atoi(parts[1])
                day,  _ := strconv.Atoi(parts[2])
                start_date := time.Date(year, time.Month(month), day,
                        0, 0, 0, 0, time.Local)
                all_events = filter_after_date(all_events, start_date)
        }
        events := drop_consecutive_events(all_events)
        calculate_durations(events)
        calculate_total_durations(events)
        by_day := split_by_day(events)
        backfill_first_day(&by_day[0])
        by_week := group_by_week(by_day)
        report := generate_report(by_week)
        report.SearchTerm = search_term
        report.SearchDate = search_date
        err = execute_template("search.template", report, w)
        if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
        }
        return
}

func log_handler(w http.ResponseWriter, r *http.Request) {
	err := execute_template("log.template", nil, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func write_to_log(line []byte) error {
	log_file, err := os.OpenFile(*log_filename, os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return errors.New(fmt.Sprint("Couldn't open log file: ", err))
	}
	defer log_file.Close() // Closed with error checking below
	written, err := log_file.Write(line)
	if err != nil {
		if written == 0 {
			return errors.New(fmt.Sprint("Couldn't write to log file: ", err))
		} else {
			return errors.New(fmt.Sprint("Only wrote ", written, " bytes to log file: ", err))
		}
	}
	err = log_file.Close()
	if err != nil {
		return errors.New(fmt.Sprint("Couldn't close log file: ", err))
	}
	return nil
}

func log_submit_handler(w http.ResponseWriter, r *http.Request) {
	w.Header()["Allow"] = []string{"POST"}
	if r.Method != "POST" {
		http.Error(w, "Please use POST", http.StatusMethodNotAllowed)
		return
	}
	t := time.Now().Format("2006 01 02 15 04 05 ")
	thing := strings.Replace(r.FormValue("thing"), "\n", "", -1)
	err := write_to_log([]byte(t + thing + "\n"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = execute_template("log_submit.template", nil, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func main() {
	flag.Parse()
	http.HandleFunc("/view", view_handler)
        http.HandleFunc("/search", search_handler)
	http.HandleFunc("/log", log_handler)
	http.HandleFunc("/log_submit", log_submit_handler)
	err := http.ListenAndServe(*listen_address+":"+strconv.Itoa(*port), nil)
	if err != nil {
		log.Fatal("http.ListenAndServe: ", err)
	}
}
