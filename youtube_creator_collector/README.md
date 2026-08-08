# YouTube PC Game Creator Collector

A comprehensive data collection and analysis framework for YouTube PC gaming content creators.

## Features

- **Multi-source Data Collection**: Collect data from YouTube API and SocialBlade
- **Advanced Filtering**: Filter by region, activity, engagement, and contact availability
- **Comprehensive Analysis**: Activity scoring, stability analysis, engagement metrics
- **Multiple Export Formats**: Export to CSV, JSON, and Excel formats
- **Scalable Architecture**: Modular design for easy extension

## Installation

```bash
# Clone the repository
git clone <repository-url>
cd youtube_creator_collector

# Create virtual environment
python -m venv venv
source venv/bin/activate  # On Windows: venv\Scripts\activate

# Install dependencies
pip install -r requirements.txt
```

## Configuration

1. Copy `config/settings.py` and customize your settings
2. Set environment variables:
   - `YOUTUBE_API_KEY`: Your YouTube Data API key
   - Other settings can be configured in `config/settings.py`

## Usage

### Basic Usage

```python
from main import YouTubeCreatorCollector

# Initialize and run collection
collector = YouTubeCreatorCollector()
results = collector.run_collection_pipeline()
```

### Custom Analysis

```python
from config.settings import Settings
from src.collectors.youtube_collector import YouTubeCollector
from src.analyzers.creator_analyzer import CreatorAnalyzer

# Custom configuration
settings = Settings()

# Collect creators
collector = YouTubeCollector(settings)
creators = collector.collect_top_creators(
    category='gaming',
    game_category='pc',
    max_results=1000
)

# Analyze with custom criteria
analyzer = CreatorAnalyzer(settings)
qualified = analyzer.analyze_creators(
    creators,
    criteria={
        'active_days': 30,
        'min_view_ratio': 0.60,
        'regions': ['US', 'CA', 'GB', 'AU', 'NZ', 'SG'],
        'has_email': True
    }
)
```

### Export Results

```python
from src.exporters.data_exporter import DataExporter

exporter = DataExporter(settings)
results = exporter.export_results(
    qualified_creators,
    formats=['csv', 'json', 'excel'],
    output_dir='output/',
    options={'target_regions': ['US', 'CA', 'GB', 'AU', 'NZ', 'SG']}
)
```

## Project Structure

```
youtube_creator_collector/
├── main.py                    # Main entry point
├── config/
│   ├── __init__.py
│   └── settings.py            # Configuration management
├── src/
│   ├── __init__.py
│   ├── collectors/
│   │   ├── __init__.py
│   │   ├── youtube_collector.py      # YouTube API collector
│   │   └── socialblade_collector.py  # SocialBlade collector
│   ├── parsers/
│   │   ├── __init__.py
│   │   └── data_parser.py     # Data parsing and cleaning
│   ├── analyzers/
│   │   ├── __init__.py
│   │   └── creator_analyzer.py # Creator analysis
│   ├── exporters/
│   │   ├── __init__.py
│   │   └── data_exporter.py   # Data export
│   └── models/
│       ├── __init__.py
│       └── creator.py         # Data models
├── data/
│   ├── output/                # Export output directory
│   ├── logs/                  # Log files
│   └── cache/                 # Cache directory
├── requirements.txt
└── README.md
```

## Configuration Options

### Target Regions

The tool targets creators from these regions:
- US (United States)
- CA (Canada)
- GB (United Kingdom)
- AU (Australia)
- NZ (New Zealand)
- SG (Singapore)

### Selection Criteria

Creators are filtered based on:
- **Activity**: Published content within the last 30 days
- **Region Relevance**: At least 60% viewership from target regions
- **Contact Availability**: Has valid business email
- **Stability**: Consistent growth patterns

## API Requirements

### YouTube Data API v3

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create a new project or select existing one
3. Enable YouTube Data API v3
4. Create API credentials (API key)
5. Set the API key in environment or config

### SocialBlade

SocialBlade data is collected via web scraping. Please respect their [Terms of Service](https://socialblade.com/terms).

## License

MIT License

## Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/AmazingFeature`)
3. Commit your changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request
