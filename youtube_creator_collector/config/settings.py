"""
配置模块 - 项目配置管理
=========================

管理所有采集和分析的配置参数。
"""

from pathlib import Path
from typing import List, Dict, Optional
import json
import os

class Settings:
    """
    项目配置类
    集中管理所有配置参数，支持环境变量覆盖
    """
    
    # 目标地区列表
    TARGET_REGIONS = [
        'US',  # 美国
        'CA',  # 加拿大
        'GB',  # 英国
        'AU',  # 澳大利亚
        'NZ',  # 新西兰
        'SG'   # 新加坡
    ]
    
    # 地区名称映射
    REGION_NAMES = {
        'US': 'United States',
        'CA': 'Canada', 
        'GB': 'United Kingdom',
        'AU': 'Australia',
        'NZ': 'New Zealand',
        'SG': 'Singapore'
    }
    
    def __init__(self, config_file: Optional[str] = None):
        """
        初始化配置
        
        Args:
            config_file: 配置文件路径（可选）
        """
        self.PROJECT_ROOT = Path(__file__).parent.parent
        self.DATA_DIR = self.PROJECT_ROOT / 'data'
        self.OUTPUT_DIR = self.DATA_DIR / 'output'
        self.LOG_DIR = self.DATA_DIR / 'logs'
        self.CACHE_DIR = self.DATA_DIR / 'cache'
        
        # YouTube API配置
        self.YOUTUBE_API_KEY = os.getenv('YOUTUBE_API_KEY', '')
        self.YOUTUBE_API_BASE_URL = 'https://www.googleapis.com/youtube/v3'
        self.YOUTUBE_MAX_RESULTS_PER_PAGE = 50
        self.YOUTUBE_MAX_CREATORS = 10000
        
        # SocialBlade配置
        self.SOCIALBLADE_BASE_URL = 'https://socialblade.com'
        self.SOCIALBLADE_REQUEST_DELAY = 2.0  # 请求间隔（秒）
        
        # 数据采集配置
        self.MIN_ACTIVE_DAYS = 30  # 最小活跃天数
        self.MIN_VIEW_RATIO = 0.60  # 目标地区最小观看占比
        self.MIN_EMAIL_AVAILABLE = True  # 必须有邮箱
        
        # 请求配置
        self.REQUEST_TIMEOUT = 30  # 请求超时时间（秒）
        self.REQUEST_RETRY_COUNT = 3  # 请求重试次数
        self.REQUEST_DELAY = 1.0  # 请求间隔（秒）
        
        # 分析配置
        self.STABILITY_WINDOW_DAYS = 7  # 稳定性分析窗口
        self.GROWTH_THRESHOLD = 0.1  # 增长率阈值
        
        # 数据库配置
        self.DATABASE_PATH = self.DATA_DIR / 'creators.db'
        self.DATABASE_BACKUP_DIR = self.DATA_DIR / 'backups'
        
        # 导出配置
        self.EXPORT_FORMATS = ['csv', 'json', 'excel']
        self.CSV_DELIMITER = ','
        self.JSON_INDENT = 2
        
        # 负载均衡配置
        self.MAX_CONCURRENT_REQUESTS = 5
        
        # 加载自定义配置
        if config_file and os.path.exists(config_file):
            self._load_config_file(config_file)
            
        # 创建必要目录
        self._create_directories()
        
    def _create_directories(self):
        """创建必要的目录结构"""
        directories = [
            self.DATA_DIR,
            self.OUTPUT_DIR,
            self.LOG_DIR,
            self.CACHE_DIR,
            self.DATABASE_BACKUP_DIR
        ]
        
        for directory in directories:
            directory.mkdir(parents=True, exist_ok=True)
            
    def _load_config_file(self, config_file: str):
        """
        加载配置文件
        
        Args:
            config_file: 配置文件路径
        """
        try:
            with open(config_file, 'r', encoding='utf-8') as f:
                config = json.load(f)
                
            # 更新配置
            for key, value in config.items():
                if hasattr(self, key):
                    setattr(self, key, value)
                    
        except Exception as e:
            print(f"Warning: Failed to load config file: {e}")
            
    def get_region_name(self, region_code: str) -> str:
        """
        获取地区名称
        
        Args:
            region_code: 地区代码
            
        Returns:
            地区名称
        """
        return self.REGION_NAMES.get(region_code, region_code)
        
    def get_target_regions_str(self) -> str:
        """
        获取目标地区字符串（用于API请求）
        
        Returns:
            逗号分隔的地区代码字符串
        """
        return ','.join(self.TARGET_REGIONS)
        
    def get_youtube_headers(self) -> Dict[str, str]:
        """
        获取YouTube API请求头
        
        Returns:
            请求头字典
        """
        return {
            'Authorization': f'Bearer {self.YOUTUBE_API_KEY}',
            'Accept': 'application/json'
        }
        
    def get_socialblade_headers(self) -> Dict[str, str]:
        """
        获取SocialBlade请求头
        
        Returns:
            请求头字典
        """
        return {
            'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36',
            'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
            'Accept-Language': 'en-US,en;q=0.5'
        }
        
    def validate(self) -> bool:
        """
        验证配置是否有效
        
        Returns:
            配置是否有效
        """
        errors = []
        
        if not self.YOUTUBE_API_KEY:
            errors.append("YouTube API key is not configured")
            
        if self.REQUEST_DELAY < 0.1:
            errors.append("Request delay must be at least 0.1 seconds")
            
        if self.MIN_VIEW_RATIO < 0 or self.MIN_VIEW_RATIO > 1:
            errors.append("Minimum view ratio must be between 0 and 1")
            
        if errors:
            for error in errors:
                print(f"Configuration error: {error}")
            return False
            
        return True
        
    def to_dict(self) -> Dict:
        """
        将配置转换为字典
        
        Returns:
            配置字典
        """
        return {
            'TARGET_REGIONS': self.TARGET_REGIONS,
            'REGION_NAMES': self.REGION_NAMES,
            'YOUTUBE_API_BASE_URL': self.YOUTUBE_API_BASE_URL,
            'YOUTUBE_MAX_RESULTS_PER_PAGE': self.YOUTUBE_MAX_RESULTS_PER_PAGE,
            'YOUTUBE_MAX_CREATORS': self.YOUTUBE_MAX_CREATORS,
            'MIN_ACTIVE_DAYS': self.MIN_ACTIVE_DAYS,
            'MIN_VIEW_RATIO': self.MIN_VIEW_RATIO,
            'MIN_EMAIL_AVAILABLE': self.MIN_EMAIL_AVAILABLE,
            'REQUEST_TIMEOUT': self.REQUEST_TIMEOUT,
            'REQUEST_RETRY_COUNT': self.REQUEST_RETRY_COUNT,
            'REQUEST_DELAY': self.REQUEST_DELAY,
            'DATA_DIR': str(self.DATA_DIR),
            'OUTPUT_DIR': str(self.OUTPUT_DIR),
            'LOG_DIR': str(self.LOG_DIR),
            'DATABASE_PATH': str(self.DATABASE_PATH)
        }
