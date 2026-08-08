"""
YouTube PC Game Creator Collector
=================================

A comprehensive data collection and analysis framework for YouTube PC gaming content creators.

Target Regions: United States, Canada, United Kingdom, Australia, New Zealand, Singapore
Criteria:
- Top 10000 creators
- Active in the last 30 days
- Stable view counts
- Target region viewership ratio > 60%
- Has collaboration email address

Author: YouTube Creator Analytics Team
Version: 1.0.0
"""

import sys
import logging
from pathlib import Path
from datetime import datetime

# Add project root to path
PROJECT_ROOT = Path(__file__).parent
sys.path.insert(0, str(PROJECT_ROOT))

# Import core modules
from config.settings import Settings
from src.collectors.youtube_collector import YouTubeCollector
from src.collectors.socialblade_collector import SocialBladeCollector
from src.parsers.data_parser import DataParser
from src.analyzers.creator_analyzer import CreatorAnalyzer
from src.exporters.data_exporter import DataExporter

class YouTubeCreatorCollector:
    """
    Main orchestrator class for the YouTube creator data collection process.
    """
    
    def __init__(self):
        """Initialize the collector with all required components."""
        self.settings = Settings()
        self.youtube_collector = YouTubeCollector(self.settings)
        self.socialblade_collector = SocialBladeCollector(self.settings)
        self.data_parser = DataParser(self.settings)
        self.creator_analyzer = CreatorAnalyzer(self.settings)
        self.data_exporter = DataExporter(self.settings)
        
        # Setup logging
        self._setup_logging()
        
    def _setup_logging(self):
        """Configure logging for the application."""
        log_dir = self.settings.LOG_DIR
        log_dir.mkdir(parents=True, exist_ok=True)
        
        log_file = log_dir / f"collector_{datetime.now().strftime('%Y%m%d_%H%M%S')}.log"
        
        logging.basicConfig(
            level=logging.INFO,
            format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
            handlers=[
                logging.FileHandler(log_file),
                logging.StreamHandler()
            ]
        )
        
        self.logger = logging.getLogger(__name__)
        
    def run_collection_pipeline(self):
        """
        Execute the complete data collection and analysis pipeline.
        
        Steps:
        1. Collect creator data from YouTube API
        2. Enrich data with SocialBlade statistics
        3. Parse and clean collected data
        4. Analyze creators based on criteria
        5. Export filtered results
        """
        self.logger.info("Starting YouTube Creator Collection Pipeline")
        
        try:
            # Step 1: Collect data from YouTube
            self.logger.info("Step 1: Collecting data from YouTube API")
            raw_youtube_data = self.youtube_collector.collect_top_creators(
                category='gaming',
                game_category='pc',
                max_results=10000,
                region_codes=self.settings.TARGET_REGIONS
            )
            
            # Step 2: Enrich with SocialBlade data
            self.logger.info("Step 2: Enriching data with SocialBlade statistics")
            enriched_data = self.socialblade_collector.enrich_creator_data(
                raw_youtube_data
            )
            
            # Step 3: Parse and clean data
            self.logger.info("Step 3: Parsing and cleaning data")
            cleaned_data = self.data_parser.parse_and_clean(enriched_data)
            
            # Step 4: Analyze creators
            self.logger.info("Step 4: Analyzing creators based on criteria")
            analyzed_data = self.creator_analyzer.analyze_creators(
                cleaned_data,
                criteria={
                    'active_days': 30,
                    'min_view_ratio': 0.60,
                    'regions': self.settings.TARGET_REGIONS,
                    'has_email': True
                }
            )
            
            # Step 5: Export results
            self.logger.info("Step 5: Exporting results")
            self.data_exporter.export_results(
                analyzed_data,
                formats=['csv', 'json', 'excel'],
                output_dir=self.settings.OUTPUT_DIR
            )
            
            self.logger.info("Collection pipeline completed successfully!")
            return analyzed_data
            
        except Exception as e:
            self.logger.error(f"Pipeline failed: {str(e)}")
            raise

def main():
    """Main entry point for the application."""
    collector = YouTubeCreatorCollector()
    
    # Run the collection pipeline
    results = collector.run_collection_pipeline()
    
    # Print summary
    print(f"\nCollection completed!")
    print(f"Total creators analyzed: {len(results)}")
    print(f"Results exported to: {collector.settings.OUTPUT_DIR}")

if __name__ == "__main__":
    main()
