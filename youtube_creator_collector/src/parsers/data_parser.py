"""
数据解析和清洗模块
==================

处理和清洗采集到的原始数据。
"""

import re
import json
from typing import List, Dict, Optional, Any
from datetime import datetime
import logging
from difflib import SequenceMatcher

from config.settings import Settings
from models.creator import YouTubeCreator, ChannelStatistics, RegionData, ContactInfo

class DataParser:
    """
    数据解析和清洗器
    
    负责解析原始数据并进行数据清洗
    """
    
    def __init__(self, settings: Settings):
        """
        初始化解析器
        
        Args:
            settings: 配置对象
        """
        self.settings = settings
        self.logger = logging.getLogger(__name__)
        
    def parse_and_clean(self, creators: List[YouTubeCreator]) -> List[YouTubeCreator]:
        """
        解析和清洗创作者数据
        
        Args:
            creators: 原始创作者列表
            
        Returns:
            清洗后的创作者列表
        """
        self.logger.info(f"Parsing and cleaning {len(creators)} creators")
        
        cleaned_creators = []
        
        for creator in creators:
            try:
                # 清洗每个创作者数据
                cleaned_creator = self._clean_creator(creator)
                
                if cleaned_creator:
                    cleaned_creators.append(cleaned_creator)
                    
            except Exception as e:
                self.logger.warning(f"Failed to clean creator {creator.channel_id}: {e}")
                
        self.logger.info(f"Cleaned {len(cleaned_creators)} creators")
        return cleaned_creators
    
    def _clean_creator(self, creator: YouTubeCreator) -> Optional[YouTubeCreator]:
        """
        清洗单个创作者数据
        
        Args:
            creator: 创作者对象
            
        Returns:
            清洗后的创作者对象
        """
        # 清洗频道名称
        creator.channel_name = self._clean_text(creator.channel_name)
        creator.description = self._clean_text(creator.description)
        
        # 清洗统计数据
        if creator.statistics:
            creator.statistics = self._clean_statistics(creator.statistics)
            
        # 清洗地区数据
        if creator.region_data:
            creator.region_data = self._clean_region_data(creator.region_data)
            
        # 清洗联系信息
        if creator.contact_info:
            creator.contact_info = self._clean_contact_info(creator.contact_info)
            
        # 清洗标签
        if creator.tags:
            creator.tags = self._clean_tags(creator.tags)
            
        # 验证数据完整性
        if self._validate_creator(creator):
            return creator
        else:
            return None
    
    def _clean_text(self, text: str) -> str:
        """
        清洗文本
        
        Args:
            text: 原始文本
            
        Returns:
            清洗后的文本
        """
        if not text:
            return ""
            
        # 移除多余空白
        text = ' '.join(text.split())
        
        # 移除特殊字符（保留基本标点）
        text = re.sub(r'[^\w\s\-.,!?@]', '', text)
        
        # 移除HTML标签
        text = re.sub(r'<[^>]+>', '', text)
        
        # 限制长度
        max_length = 5000
        if len(text) > max_length:
            text = text[:max_length]
            
        return text.strip()
    
    def _clean_statistics(self, stats: ChannelStatistics) -> ChannelStatistics:
        """
        清洗统计数据
        
        Args:
            stats: 原始统计数据
            
        Returns:
            清洗后的统计数据
        """
        # 确保数值合理
        stats.total_views = max(0, stats.total_views)
        stats.subscriber_count = max(0, stats.subscriber_count)
        stats.video_count = max(0, stats.video_count)
        
        # 计算派生值
        if stats.subscriber_count > 0:
            stats.view_per_subscriber = round(
                stats.total_views / stats.subscriber_count, 2
            )
        else:
            stats.view_per_subscriber = 0
            
        if stats.video_count > 0:
            stats.average_views_per_video = round(
                stats.total_views / stats.video_count, 2
            )
        else:
            stats.average_views_per_video = 0
            
        # 限制增长率范围
        stats.subscriber_growth_rate = max(-1, min(1, stats.subscriber_growth_rate))
        stats.view_growth_rate = max(-1, min(1, stats.view_growth_rate))
        
        # 计算互动率
        if stats.total_views > 0:
            stats.engagement_rate = round(stats.engagement_rate, 2)
            
        stats.last_updated = datetime.now()
        
        return stats
    
    def _clean_region_data(self, regions: List[RegionData]) -> List[RegionData]:
        """
        清洗地区数据
        
        Args:
            regions: 原始地区数据列表
            
        Returns:
            清洗后的地区数据列表
        """
        cleaned_regions = []
        
        for region in regions:
            # 清洗地区代码
            region.region_code = region.region_code.upper().strip()
            region.region_name = region.region_name.strip()
            
            # 确保数值合理
            region.view_count = max(0, region.view_count)
            region.subscriber_count = max(0, region.subscriber_count)
            region.view_percentage = max(0, min(100, region.view_percentage))
            
            # 过滤无效数据
            if region.region_code and region.region_name:
                cleaned_regions.append(region)
                
        # 按观看占比排序
        cleaned_regions.sort(key=lambda x: x.view_percentage, reverse=True)
        
        return cleaned_regions
    
    def _clean_contact_info(self, contact: ContactInfo) -> ContactInfo:
        """
        清洗联系信息
        
        Args:
            contact: 原始联系信息
            
        Returns:
            清洗后的联系信息
        """
        # 清洗邮箱
        if contact.email:
            contact.email = self._clean_email(contact.email)
            
        if contact.business_email:
            contact.business_email = self._clean_email(contact.business_email)
            
        # 清洗社交媒体链接
        if contact.twitter:
            contact.twitter = self._clean_twitter(contact.twitter)
            
        if contact.website:
            contact.website = self._clean_url(contact.website)
            
        # 更新邮箱状态
        contact.has_business_email = contact.business_email is not None
        
        return contact
    
    def _clean_email(self, email: str) -> Optional[str]:
        """
        清洗邮箱地址
        
        Args:
            email: 原始邮箱
            
        Returns:
            清洗后的邮箱
        """
        email = email.strip().lower()
        
        # 验证邮箱格式
        email_pattern = r'^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$'
        
        if re.match(email_pattern, email):
            return email
            
        return None
    
    def _clean_twitter(self, twitter: str) -> Optional[str]:
        """
        清洗Twitter用户名
        
        Args:
            twitter: 原始Twitter信息
            
        Returns:
            清洗后的用户名
        """
        # 移除URL和@符号
        twitter = twitter.strip()
        twitter = re.sub(r'^@', '', twitter)
        twitter = re.sub(r'^https?://(www\.)?twitter\.com/', '', twitter)
        
        # 验证格式
        if re.match(r'^[A-Za-z0-9_]{1,15}$', twitter):
            return twitter
            
        return None
    
    def _clean_url(self, url: str) -> Optional[str]:
        """
        清洗URL
        
        Args:
            url: 原始URL
            
        Returns:
            清洗后的URL
        """
        url = url.strip()
        
        # 添加协议
        if not url.startswith(('http://', 'https://')):
            url = 'https://' + url
            
        # 验证URL格式
        url_pattern = r'^https?://[^\s]+$'
        
        if re.match(url_pattern, url):
            return url
            
        return None
    
    def _clean_tags(self, tags: List[str]) -> List[str]:
        """
        清洗标签
        
        Args:
            tags: 原始标签列表
            
        Returns:
            清洗后的标签列表
        """
        cleaned_tags = []
        
        for tag in tags:
            tag = tag.strip().lower()
            tag = re.sub(r'[^\w\s\-]', '', tag)
            tag = ' '.join(tag.split())
            
            if tag and len(tag) <= 50:
                cleaned_tags.append(tag)
                
        # 去重并保持顺序
        seen = set()
        unique_tags = []
        
        for tag in cleaned_tags:
            if tag not in seen:
                seen.add(tag)
                unique_tags.append(tag)
                
        return unique_tags
    
    def _validate_creator(self, creator: YouTubeCreator) -> bool:
        """
        验证创作者数据完整性
        
        Args:
            creator: 创作者对象
            
        Returns:
            是否有效
        """
        # 检查必需字段
        if not creator.channel_id:
            return False
            
        if not creator.channel_name:
            return False
            
        # 验证统计数据的合理性
        if creator.statistics:
            if creator.statistics.subscriber_count < 0:
                return False
                
            if creator.statistics.total_views < 0:
                return False
                
            if creator.statistics.video_count < 0:
                return False
                
        return True
    
    def remove_duplicates(self, creators: List[YouTubeCreator]) -> List[YouTubeCreator]:
        """
        移除重复创作者
        
        Args:
            creators: 创作者列表
            
        Returns:
            去重后的列表
        """
        seen_ids = set()
        unique_creators = []
        
        for creator in creators:
            if creator.channel_id not in seen_ids:
                seen_ids.add(creator.channel_id)
                unique_creators.append(creator)
                
        removed_count = len(creators) - len(unique_creators)
        self.logger.info(f"Removed {removed_count} duplicate creators")
        
        return unique_creators
    
    def merge_creator_data(self, 
                          primary: YouTubeCreator, 
                          secondary: YouTubeCreator) -> YouTubeCreator:
        """
        合并两个创作者数据（用于多次采集的数据合并）
        
        Args:
            primary: 主要数据源
            secondary: 次要数据源
            
        Returns:
            合并后的创作者
        """
        # 保留最新的更新时间
        if secondary.last_updated > primary.last_updated:
            primary.last_updated = secondary.last_updated
            
        # 合并统计数据（取最新值）
        if secondary.statistics:
            if not primary.statistics:
                primary.statistics = secondary.statistics
            else:
                if secondary.statistics.total_views > primary.statistics.total_views:
                    primary.statistics.total_views = secondary.statistics.total_views
                if secondary.statistics.subscriber_count > primary.statistics.subscriber_count:
                    primary.statistics.subscriber_count = secondary.statistics.subscriber_count
                if secondary.statistics.video_count > primary.statistics.video_count:
                    primary.statistics.video_count = secondary.statistics.video_count
                    
        # 合并地区数据
        if secondary.region_data:
            primary.region_data = secondary.region_data
            
        # 合并联系信息（优先使用有邮箱的）
        if secondary.contact_info and secondary.contact_info.has_valid_email():
            if not primary.contact_info or not primary.contact_info.has_valid_email():
                primary.contact_info = secondary.contact_info
                
        # 更新采集时间
        primary.collected_at = datetime.now()
        
        return primary
    
    def parse_from_json(self, json_data: str) -> List[YouTubeCreator]:
        """
        从JSON数据解析创作者列表
        
        Args:
            json_data: JSON字符串
            
        Returns:
            创作者列表
        """
        try:
            data = json.loads(json_data)
            
            if isinstance(data, list):
                return [self._parse_single_json(creator_data) for creator_data in data]
            else:
                return [self._parse_single_json(data)]
                
        except json.JSONDecodeError as e:
            self.logger.error(f"Failed to parse JSON: {e}")
            return []
    
    def _parse_single_json(self, data: Dict) -> Optional[YouTubeCreator]:
        """
        解析单个JSON对象为创作者
        
        Args:
            data: JSON数据
            
        Returns:
            创作者对象
        """
        try:
            # 解析统计数据
            stats_data = data.get('statistics', {})
            statistics = ChannelStatistics(
                total_views=stats_data.get('total_views', 0),
                subscriber_count=stats_data.get('subscriber_count', 0),
                video_count=stats_data.get('video_count', 0),
                view_per_subscriber=stats_data.get('view_per_subscriber', 0),
                subscriber_growth_rate=stats_data.get('subscriber_growth_rate', 0),
                view_growth_rate=stats_data.get('view_growth_rate', 0),
                average_views_per_video=stats_data.get('average_views_per_video', 0),
                engagement_rate=stats_data.get('engagement_rate', 0)
            )
            
            # 解析地区数据
            region_data = []
            for r in data.get('region_data', []):
                region_data.append(RegionData(
                    region_code=r.get('region_code', ''),
                    region_name=r.get('region_name', ''),
                    view_count=r.get('view_count', 0),
                    subscriber_count=r.get('subscriber_count', 0),
                    view_percentage=r.get('view_percentage', 0)
                ))
                
            # 解析联系信息
            contact_data = data.get('contact_info', {})
            contact_info = ContactInfo(
                email=contact_data.get('email'),
                business_email=contact_data.get('business_email'),
                twitter=contact_data.get('twitter'),
                instagram=contact_data.get('instagram'),
                website=contact_data.get('website')
            )
            
            # 创建创作者对象
            return YouTubeCreator(
                channel_id=data.get('channel_id', ''),
                channel_name=data.get('channel_name', ''),
                custom_url=data.get('custom_url'),
                description=data.get('description', ''),
                thumbnail_url=data.get('thumbnail_url'),
                banner_url=data.get('banner_url'),
                statistics=statistics,
                region_data=region_data,
                contact_info=contact_info,
                pc_game_focus=data.get('pc_game_focus', True),
                tags=data.get('tags', [])
            )
            
        except Exception as e:
            self.logger.error(f"Error parsing creator JSON: {e}")
            return None
