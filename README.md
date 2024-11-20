# weather

This service provides a simplified way to retrieve weather information from different weather APIs. It handles both Canadian (via Environment Canada) and American (via NOAA) weather conditions & forecasts.

## Proto Generation

Use `protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative weather.proto` to regenerate the .pb.go files.