package envcan

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/rmrobinson/weather"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	// ErrInvalidDate is returned if an invalid date qualifier is supplied.
	ErrInvalidDate   = errors.New("invalid date supplied")
	refreshFrequency = time.Minute * 30
)

// Station contains the data about a single weather location reported on by Environment Canada
type Station struct {
	url       string
	title     string
	latitude  float64
	longitude float64

	logger *zap.Logger

	currentReport *weather.WeatherReport
	forecast      []*weather.WeatherForecast
	lastRefreshed time.Time
}

func NewStation(logger *zap.Logger, url string, title string, lat float64, lon float64) *Station {
	return &Station{
		url:       url,
		title:     title,
		latitude:  lat,
		longitude: lon,
		logger:    logger,
	}
}

// Name returns the printable name of this weather station
func (s *Station) Name() string {
	return s.title
}

// Latitude returns the latitude of this weather station
func (s *Station) Latitude() float64 {
	return s.latitude
}

// Longitude returns the longitude of this weather station
func (s *Station) Longitude() float64 {
	return s.longitude
}

// GetReport returns the current weather report for this station.
func (s *Station) GetReport(ctx context.Context) (*weather.WeatherReport, error) {
	if s.shouldRefresh() {
		err := s.refresh(ctx)
		if err != nil {
			return nil, err
		}
	}

	return s.currentReport, nil
}

// GetForecast returns the forecast for this station
func (s *Station) GetForecast(ctx context.Context) ([]*weather.WeatherForecast, error) {
	if s.shouldRefresh() {
		err := s.refresh(ctx)
		if err != nil {
			return nil, err
		}
	}

	return s.forecast, nil
}

func (s *Station) shouldRefresh() bool {
	return time.Now().Add(refreshFrequency * -1).After(s.lastRefreshed)
}

func (s *Station) refresh(ctx context.Context) error {
	feed, err := s.getFeed(ctx)
	if err != nil {
		s.logger.Warn("error getting feed",
			zap.Error(err),
		)
		return err
	}

	report, forecast, err := s.parseFeed(feed)
	if err != nil {
		s.logger.Warn("error parsing feed",
			zap.Error(err),
		)
		return err
	}

	s.currentReport = report
	s.forecast = forecast
	s.lastRefreshed = time.Now()

	s.logger.Debug("refreshed station",
		zap.String("station_title", s.title),
	)

	return nil
}

func (s *Station) getFeed(ctx context.Context) (*gofeed.Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		s.logger.Warn("error creating new request",
			zap.Error(err),
		)
		return nil, err
	}

	client := http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.Warn("error performing request",
			zap.Error(err),
		)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.logger.Info("received non-OK response",
			zap.Int("status_code", resp.StatusCode),
		)
		return nil, nil
	}

	fp := gofeed.NewParser()
	feed, err := fp.Parse(resp.Body)
	if err != nil {
		s.logger.Warn("error parsing feed",
			zap.Error(err),
		)
		return nil, err
	}

	return feed, nil
}

func (s *Station) parseFeed(feed *gofeed.Feed) (*weather.WeatherReport, []*weather.WeatherForecast, error) {
	report := &weather.WeatherReport{
		Conditions: &weather.WeatherCondition{},
	}
	var forecasts []*weather.WeatherForecast

	for _, item := range feed.Items {
		for _, category := range item.Categories {
			if category == "Current Conditions" {
				report.ObservedAt = timestamppb.New(*item.UpdatedParsed)
				report.ObservationId = item.GUID
				report.CreatedAt = timestamppb.New(*item.PublishedParsed)
				report.UpdatedAt = timestamppb.New(*item.UpdatedParsed)

				report.Conditions = currentConditionsToCondition(item.Description)
			} else if category == "Weather Forecasts" {
				forecast := &weather.WeatherForecast{
					ForecastId: item.GUID,
					Conditions: forecastConditionToCondition(item.Description),
				}

				forecast.CreatedAt = timestamppb.New(*item.PublishedParsed)
				forecast.UpdatedAt = timestamppb.New(*item.UpdatedParsed)

				forecastDayOfWeek := strings.Split(item.Title, ":")
				forecastDayOfWeek = strings.Split(forecastDayOfWeek[0], " ")
				forecastFor, err := futureDateFromFeedDate(*item.PublishedParsed, forecastDayOfWeek[0])

				if err == nil {
					if len(forecastDayOfWeek) > 1 && forecastDayOfWeek[1] == "night" {
						forecastFor = time.Date(forecastFor.Year(), forecastFor.Month(), forecastFor.Day(), 23, 0, 0, 0, forecastFor.Location())
					} else {
						forecastFor = time.Date(forecastFor.Year(), forecastFor.Month(), forecastFor.Day(), 12, 0, 0, 0, forecastFor.Location())
					}

					forecast.ForecastedFor = timestamppb.New(forecastFor)
				}

				forecasts = append(forecasts, forecast)
			}
		}
	}

	return report, forecasts, nil
}

func currentConditionsToCondition(cc string) *weather.WeatherCondition {
	cond := &weather.WeatherCondition{}

	records := strings.Split(cc, "<br/>")
	for _, record := range records {
		record = strings.TrimSpace(record)

		record = strings.Replace(record, "<b>", "", -1)
		record = strings.Replace(record, "</b>", "", -1)
		recordParts := strings.Split(record, ":")

		if len(recordParts) != 2 {
			continue
		}

		switch recordParts[0] {
		case "Condition":
			cond.Summary = strings.TrimSpace(recordParts[1])
			cond.SummaryIcon = iconFromFeedText(cond.Summary)
		case "Temperature":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, "&deg;C", "", -1)

			val, _ := strconv.ParseFloat(str, 32)
			cond.Temperature = float32(val)
		case "Wind Chill":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, "&deg;C", "", -1)

			val, _ := strconv.ParseFloat(str, 32)
			cond.WindChill = float32(val)
		case "Dewpoint":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, "&deg;C", "", -1)

			val, _ := strconv.ParseFloat(str, 32)
			cond.DewPoint = float32(val)
		case "Pressure":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, " kPa", "", -1)

			val, _ := strconv.ParseFloat(str, 32)
			cond.Pressure = float32(val)
		case "Visibility":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, " km", "", -1)

			val, _ := strconv.ParseFloat(str, 32)
			cond.Visibility = int32(val)
		case "Humidity":
			str := strings.TrimSpace(recordParts[1])
			str = strings.Replace(str, " %", "", -1)

			val, _ := strconv.ParseInt(str, 10, 32)
			cond.Humidity = int32(val)
		case "Wind":
			str := strings.TrimSpace(recordParts[1])

			parts := strings.Split(str, " ")
			if len(parts) == 2 {
				// i.e. 10 km/h
				str = parts[0]
			} else if len(parts) == 3 {
				// i.e. ESE 10 km/h
				str = parts[1]
			}

			val, _ := strconv.ParseInt(str, 10, 32)
			cond.WindSpeed = int32(val)
		}
	}

	return cond
}

func forecastConditionToCondition(fc string) *weather.WeatherCondition {
	cond := &weather.WeatherCondition{}
	records := strings.Split(fc, ".")
	for idx, record := range records {
		record = strings.TrimSpace(record)

		if idx == 0 {
			cond.Summary = record
			cond.SummaryIcon = iconFromFeedText(record)
			continue
		}

		if strings.HasPrefix(record, "Wind chill") {
			val, err := floatFromFeedText(record)
			if err == nil {
				cond.WindChill = val
			}
		} else if strings.HasPrefix(record, "Wind") {
			strippedRecord := strings.TrimPrefix(record, "Wind ")
			fields := strings.Split(strippedRecord, " ")
			for fieldIdx, field := range fields {
				if field == "km/h" && fieldIdx != 0 {
					val, err := strconv.ParseInt(fields[fieldIdx-1], 10, 32)
					if err == nil {
						cond.WindSpeed = int32(val)
						break
					}
				}
			}
		} else if strings.HasPrefix(record, "UV index") {
			strippedRecord := strings.TrimPrefix(record, "UV index ")
			fields := strings.Split(strippedRecord, " ")

			val, err := strconv.ParseInt(fields[0], 10, 8)
			if err == nil {
				cond.UvIndex = int32(val)
			}
		} else if strings.HasPrefix(record, "High") ||
			strings.HasPrefix(record, "Low") ||
			strings.HasPrefix(record, "Temperature") {
			val, err := floatFromFeedText(record)
			if err == nil {
				cond.Temperature = val
			}
		}
	}

	return cond
}

func iconFromFeedText(text string) weather.WeatherIcon {
	text = strings.ToLower(text)
	if strings.Contains(text, "snow") || strings.Contains(text, "flurries") {
		return weather.WeatherIcon_SNOW
	}

	if strings.Contains(text, "rain") {
		if strings.Contains(text, "chance") || strings.Contains(text, "partially") {
			return weather.WeatherIcon_CHANCE_OF_RAIN
		} else if strings.Contains(text, "storm") || strings.Contains(text, "lightning") {
			return weather.WeatherIcon_THUNDERSTORMS
		}
		return weather.WeatherIcon_RAIN
	}

	if strings.Contains(text, "thunder") {
		return weather.WeatherIcon_THUNDERSTORMS
	}

	if strings.Contains(text, "cloud") {
		if strings.Contains(text, "partially") {
			return weather.WeatherIcon_PARTIALLY_CLOUDY
		} else if strings.Contains(text, "sun") {
			return weather.WeatherIcon_MOSTLY_CLOUDY
		}
		return weather.WeatherIcon_CLOUDY
	}

	if strings.Contains(text, "fog") || strings.Contains(text, "mist") {
		return weather.WeatherIcon_FOG
	}
	if strings.Contains(text, "sunny") {
		if strings.Contains(text, "partially") {
			return weather.WeatherIcon_PARTIALLY_CLOUDY
		}
		return weather.WeatherIcon_SUNNY
	}

	return weather.WeatherIcon_SUNNY
}

func floatFromFeedText(input string) (float32, error) {
	ret := float32(0)
	retSet := false

	fields := strings.Split(input, " ")
	for fieldIdx, field := range fields {
		val, err := strconv.ParseFloat(field, 32)
		if err != nil {
			continue
		}

		if fieldIdx != 0 && fields[fieldIdx-1] == "minus" {
			val *= -1
		}

		ret = float32(val)
		retSet = true
	}

	if retSet {
		return ret, nil
	}
	return 0, errors.New("no value present")
}

func futureDateFromFeedDate(startDate time.Time, futureDayOfWeek string) (time.Time, error) {
	futureDayOfWeek = strings.ToLower(futureDayOfWeek)
	startDayOfWeek := strings.ToLower(startDate.Format("Monday"))

	futureDayIdx := -1
	for idx, day := range dayOfWeek {
		if day == futureDayOfWeek {
			futureDayIdx = idx
			break
		}
	}

	startDayIdx := -1
	for idx, day := range dayOfWeek {
		if day == startDayOfWeek {
			startDayIdx = idx
			break
		}
	}

	if futureDayIdx < 0 || startDayIdx < 0 {
		return startDate, ErrInvalidDate
	}

	delta := deltaBetweenDays[startDayIdx][futureDayIdx]

	futureDate := startDate.AddDate(0, 0, delta)
	return futureDate, nil
}

var dayOfWeek = []string{
	"sunday",
	"monday",
	"tuesday",
	"wednesday",
	"thursday",
	"friday",
	"saturday",
}

var deltaBetweenDays = [][]int{
	// sunday
	{
		0,
		1,
		2,
		3,
		4,
		5,
		6,
	},
	// monday
	{
		6,
		0,
		1,
		2,
		3,
		4,
		5,
	},
	// tuesday
	{
		5,
		6,
		0,
		1,
		2,
		3,
		4,
	},
	// wednesday
	{
		4,
		5,
		6,
		0,
		1,
		2,
		3,
	},
	// thursday
	{
		3,
		4,
		5,
		6,
		0,
		1,
		2,
	},
	// friday
	{
		2,
		3,
		4,
		5,
		6,
		0,
		1,
	},
	// saturday
	{
		1,
		2,
		3,
		4,
		5,
		6,
		0,
	},
}
