"""
数据模型模块
============

定义创作者和相关数据的数据模型。
"""

from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import List, Dict, Optional
from enum import Enum

class CreatorStatus(Enum):
    """创作者状态枚举"""
    ACTIVE = "active"
    INACTIVE = "inactive"
    SUSPENDED = "suspended"
    UNKNOWN = "unknown"

class ContentCategory(Enum):
    """内容类别枚举"""
    GAMING = "gaming"
    ENTERTAINMENT = "entertainment"
    EDUCATION = "education"
    TECHNOLOGY = "technology"
    OTHER = "other"

class Platform(Enum):
    """平台枚举"""
    YOUTUBE = "youtube"
    TWITCH = "twitch"
    TIKTOK = "tiktok"
    OTHER = "other"

@dataclass
class RegionData:
    """地区数据"""
    region_code: str
    region_name: str
    view_count: int
    subscriber_count: int
    view_percentage: float
    last_updated: datetime = field(default_factory=datetime.now)
    
    def __post_init__(self):
        if self.view_percentage < 0:
            self.view_percentage = 0
        elif self.view_percentage > 100:
            self.view_percentage = 100

@dataclass
class ChannelStatistics:
    """频道统计数据"""
    total_views: int
    subscriber_count: int
    video_count: int
    view_per_subscriber: float
    subscriber_growth_rate: float
    view_growth_rate: float
    average_views_per_video: int
    engagement_rate: float
    last_updated: datetime = field(default_factory=datetime.now)
    
    def calculate_stability_score(self, window_days: int = 7) -> float:
        """
        计算稳定性评分
        
        Args:
            window_days: 分析窗口天数
            
        Returns:
            稳定性评分 (0-100)
        """
        # 基于增长率波动计算稳定性
        growth_fluctuation = abs(self.subscriber_growth_rate - self.view_growth_rate)
        stability = max(0, 100 - (growth_fluctuation * 100))
        return stability
    
    def is_stable(self, threshold: float = 0.1) -> bool:
        """
        判断是否稳定
        
        Args:
            threshold: 稳定阈值
            
        Returns:
            是否稳定
        """
        return abs(self.subscriber_growth_rate) < threshold and \
               abs(self.view_growth_rate) < threshold

@dataclass
class ContactInfo:
    """联系信息"""
    email: Optional[str] = None
    business_email: Optional[str] = None
    twitter: Optional[str] = None
    instagram: Optional[str] = None
    website: Optional[str] = None
    has_business_email: bool = False
    
    def has_valid_email(self) -> bool:
        """检查是否有有效的邮箱"""
        return self.email is not None or self.business_email is not None

@dataclass
class VideoData:
    """视频数据"""
    video_id: str
    title: str
    published_at: datetime
    view_count: int
    like_count: int
    comment_count: int
    duration_seconds: int
    category: str = "gaming"
    is_pc_game_content: bool = True
    last_updated: datetime = field(default_factory=datetime.now)
    
    def get_engagement_rate(self) -> float:
        """
        计算互动率
        
        Returns:
            互动率
        """
        if self.view_count == 0:
            return 0
        return ((self.like_count + self.comment_count) / self.view_count) * 100

@dataclass
class ActivityData:
    """活跃度数据"""
    last_video_date: Optional[datetime]
    total_videos_last_30_days: int
    total_videos_last_90_days: int
    upload_frequency_per_week: float
    average_days_between_uploads: float
    last_updated: datetime = field(default_factory=datetime.now)
    
    def is_active_in_last_days(self, days: int = 30) -> bool:
        """
        检查是否在指定天数内活跃
        
        Args:
            days: 天数
            
        Returns:
            是否活跃
        """
        if self.last_video_date is None:
            return False
        
        days_since_last_video = (datetime.now() - self.last_video_date).days
        return days_since_last_video <= days
    
    def get_activity_score(self) -> float:
        """
        计算活跃度评分
        
        Returns:
            活跃度评分 (0-100)
        """
        if self.total_videos_last_30_days == 0:
            return 0
        
        # 基于视频数量和上传频率计算活跃度
        score = min(100, self.total_videos_last_30_days * 10)
        score += min(20, self.upload_frequency_per_week * 2)
        return min(100, score)

@dataclass
class YouTubeCreator:
    """
    YouTube创作者数据模型
    
    Attributes:
        channel_id: YouTube频道ID
        channel_name: 频道名称
        custom_url: 自定义URL
        description: 频道描述
        thumbnail_url: 头像URL
        banner_url: 横幅URL
        statistics: 频道统计数据
        region_data: 各地区数据列表
        contact_info: 联系信息
        activity_data: 活跃度数据
        recent_videos: 最近视频列表
        status: 创作者状态
        content_category: 内容类别
        pc_game_focus: 是否专注PC游戏
        tags: 频道标签
        created_at: 创建时间
        last_updated: 最后更新时间
        collected_at: 数据采集时间
    """
    channel_id: str
    channel_name: str
    custom_url: Optional[str] = None
    description: str = ""
    thumbnail_url: Optional[str] = None
    banner_url: Optional[str] = None
    statistics: Optional[ChannelStatistics] = None
    region_data: List[RegionData] = field(default_factory=list)
    contact_info: Optional[ContactInfo] = None
    activity_data: Optional[ActivityData] = None
    recent_videos: List[VideoData] = field(default_factory=list)
    status: CreatorStatus = CreatorStatus.UNKNOWN
    content_category: ContentCategory = ContentCategory.GAMING
    pc_game_focus: bool = True
    tags: List[str] = field(default_factory=list)
    created_at: Optional[datetime] = None
    last_updated: datetime = field(default_factory=datetime.now)
    collected_at: datetime = field(default_factory=datetime.now)
    
    def get_target_region_view_percentage(self, target_regions: List[str]) -> float:
        """
        获取目标地区的观看占比
        
        Args:
            target_regions: 目标地区列表
            
        Returns:
            目标地区观看占比
        """
        if not self.statistics or not self.region_data:
            return 0
        
        total_target_views = 0
        total_all_views = self.statistics.total_views
        
        if total_all_views == 0:
            return 0
        
        for region in self.region_data:
            if region.region_code in target_regions:
                total_target_views += region.view_count
                
        return total_target_views / total_all_views
    
    def meets_criteria(self,
                      min_active_days: int = 30,
                      min_view_ratio: float = 0.60,
                      target_regions: Optional[List[str]] = None,
                      require_email: bool = True) -> tuple:
        """
        检查是否满足筛选条件
        
        Args:
            min_active_days: 最小活跃天数
            min_view_ratio: 最小观看占比
            target_regions: 目标地区列表
            require_email: 是否需要邮箱
            
        Returns:
            (是否满足, 不满足原因列表)
        """
        reasons = []
        
        # 检查活跃度
        if target_regions:
            view_ratio = self.get_target_region_view_percentage(target_regions)
            if view_ratio < min_view_ratio:
                reasons.append(f"目标地区观看占比不足: {view_ratio:.2%} < {min_view_ratio:.2%}")
        
        # 检查邮箱
        if require_email:
            if not self.contact_info or not self.contact_info.has_valid_email():
                reasons.append("缺少有效的联系邮箱")
        
        # 检查状态
        if self.status == CreatorStatus.INACTIVE:
            reasons.append("创作者状态为不活跃")
            
        return (len(reasons) == 0, reasons)
    
    def to_dict(self) -> Dict:
        """
        转换为字典
        
        Returns:
            字典表示
        """
        return {
            'channel_id': self.channel_id,
            'channel_name': self.channel_name,
            'custom_url': self.custom_url,
            'description': self.description,
            'thumbnail_url': self.thumbnail_url,
            'statistics': {
                'total_views': self.statistics.total_views if self.statistics else None,
                'subscriber_count': self.statistics.subscriber_count if self.statistics else None,
                'video_count': self.statistics.video_count if self.statistics else None,
                'view_per_subscriber': self.statistics.view_per_subscriber if self.statistics else None,
                'subscriber_growth_rate': self.statistics.subscriber_growth_rate if self.statistics else None,
                'view_growth_rate': self.statistics.view_growth_rate if self.statistics else None,
                'average_views_per_video': self.statistics.average_views_per_video if self.statistics else None,
                'engagement_rate': self.statistics.engagement_rate if self.statistics else None,
            } if self.statistics else None,
            'region_data': [
                {
                    'region_code': r.region_code,
                    'region_name': r.region_name,
                    'view_count': r.view_count,
                    'subscriber_count': r.subscriber_count,
                    'view_percentage': r.view_percentage,
                } for r in self.region_data
            ],
            'contact_info': {
                'email': self.contact_info.email if self.contact_info else None,
                'business_email': self.contact_info.business_email if self.contact_info else None,
                'twitter': self.contact_info.twitter if self.contact_info else None,
                'instagram': self.contact_info.instagram if self.contact_info else None,
                'website': self.contact_info.website if self.contact_info else None,
            } if self.contact_info else None,
            'activity_data': {
                'last_video_date': self.activity_data.last_video_date.isoformat() if self.activity_data and self.activity_data.last_video_date else None,
                'total_videos_last_30_days': self.activity_data.total_videos_last_30_days if self.activity_data else None,
                'total_videos_last_90_days': self.activity_data.total_videos_last_90_days if self.activity_data else None,
                'upload_frequency_per_week': self.activity_data.upload_frequency_per_week if self.activity_data else None,
            } if self.activity_data else None,
            'status': self.status.value if self.status else None,
            'content_category': self.content_category.value if self.content_category else None,
            'pc_game_focus': self.pc_game_focus,
            'tags': self.tags,
            'last_updated': self.last_updated.isoformat(),
            'collected_at': self.collected_at.isoformat()
        }
