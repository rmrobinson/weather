package weather

import (
	"context"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// ErrLocationNotFound is returned if the supplied lat/lon value can't be found.
	ErrLocationNotFound = status.New(codes.NotFound, "location not found")
)

// Station represents a single weather station location.
type Station interface {
	Name() string
	Latitude() float64
	Longitude() float64
	GetReport(ctx context.Context) (*WeatherReport, error)
	GetForecast(ctx context.Context) ([]*WeatherForecast, error)
}

// API is an implementation of the WeatherService server.
type API struct {
	UnsafeWeatherServiceServer

	logger   *zap.Logger
	stations *GeoSet
}

// NewAPI creates a new weather service server.
func NewAPI(logger *zap.Logger) *API {
	return &API{
		logger:   logger,
		stations: NewGeoSet(),
	}
}

// RegisterStation takes the supplied station and adds it to the queryable set.
func (api *API) RegisterStation(s Station) {
	api.stations.Add(s.Latitude(), s.Longitude(), s)
}

// GetCurrentReport gets a weather report
func (api *API) GetCurrentReport(ctx context.Context, req *GetCurrentReportRequest) (*GetCurrentReportResponse, error) {
	s := api.stations.Closest(req.Latitude, req.Longitude).(Station)
	if s == nil {
		return nil, ErrLocationNotFound.Err()
	}

	report, err := s.GetReport(ctx)
	if err != nil {
		api.logger.Info("error getting station report",
			zap.String("name", s.Name()),
			zap.Error(err),
		)
	}

	return &GetCurrentReportResponse{
		Report:      report,
		StationName: s.Name(),
	}, nil
}

// GetForecast gets a weather forecast.
func (api *API) GetForecast(ctx context.Context, req *GetForecastRequest) (*GetForecastResponse, error) {
	s := api.stations.Closest(req.Latitude, req.Longitude).(Station)
	if s == nil {
		return nil, ErrLocationNotFound.Err()
	}

	forecast, err := s.GetForecast(ctx)
	if err != nil {
		api.logger.Info("error getting station forecast",
			zap.String("name", s.Name()),
			zap.Error(err),
		)
		return nil, err
	}

	return &GetForecastResponse{
		ForecastRecords: forecast,
	}, nil
}
