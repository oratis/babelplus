"""
YouTube数据采集器
=================

从YouTube API采集PC游戏领域创作者数据。
"""

import time
import json
import requests
from typing import List, Dict, Optional, Iterator
from datetime import datetime, timedelta
import logging

from config.settings import Settings
from models.creator import (
    YouTubeCreator,
    ChannelStatistics,
    RegionData,
    ContactInfo,
    ActivityData,
    VideoData,
    CreatorStatus,
    ContentCategory
)

class YouTubeCollector:
    """
    YouTube数据采集器
    
    负责从YouTube API采集创作者数据
    """
    
    def __init__(self, settings: Settings):
        """
        初始化采集器
        
        Args:
            settings: 配置对象
        """
        self.settings = settings
        self.session = requests.Session()
        self.logger = logging.getLogger(__name__)
        
        # YouTube API端点
        self.API_BASE_URL = self.settings.YOUTUBE_API_BASE_URL
        self.CHANNELS_ENDPOINT = f"{self.API_BASE_URL}/channels"
        self.SEARCH_ENDPOINT = f"{self.API_BASE_URL}/search"
        self.VIDEOS_ENDPOINT = f"{self.API_BASE_URL}/videos"
        self.COMMENT_THREADS_ENDPOINT = f"{self.API_BASE_URL}/commentThreads"
        
    def _make_request(self, 
                     endpoint: str, 
                     params: Dict, 
                     max_retries: int = None) -> Optional[Dict]:
        """
        发送API请求
        
        Args:
            endpoint: API端点
            params: 请求参数
            max_retries: 最大重试次数
            
        Returns:
            响应数据
        """
        if max_retries is None:
            max_retries = self.settings.REQUEST_RETRY_COUNT
            
        for attempt in range(max_retries):
            try:
                params['key'] = self.settings.YOUTUBE_API_KEY
                
                response = self.session.get(
                    endpoint,
                    params=params,
                    timeout=self.settings.REQUEST_TIMEOUT
                )
                
                response.raise_for_status()
                
                return response.json()
                
            except requests.exceptions.RequestException as e:
                self.logger.warning(f"Request failed (attempt {attempt + 1}): {e}")
                
                if attempt < max_retries - 1:
                    time.sleep(self.settings.REQUEST_DELAY * (attempt + 1))
                else:
                    self.logger.error(f"All retries failed for {endpoint}")
                    return None
                    
        return None
    
    def collect_top_creators(self,
                           category: str = 'gaming',
                           game_category: str = 'pc',
                           max_results: int = 10000,
                           region_codes: List[str] = None) -> List[YouTubeCreator]:
        """
        采集top创作者
        
        Args:
            category: 内容类别
            game_category: 游戏类别
            max_results: 最大结果数
            region_codes: 地区代码列表
            
        Returns:
            创作者列表
        """
        self.logger.info(f"Collecting top {max_results} creators in {category}/{game_category}")
        
        creators = []
        page_token = None
        collected_count = 0
        
        while collected_count < max_results:
            # 计算每页请求数
            remaining = max_results - collected_count
            max_per_page = min(self.settings.YOUTUBE_MAX_RESULTS_PER_PAGE, remaining)
            
            # 构建搜索参数
            params = {
                'part': 'snippet',
                'q': game_category,
                'type': 'channel',
                'maxResults': max_per_page,
                'relevanceLanguage': 'en'
            }
            
            if page_token:
                params['pageToken'] = page_token
                
            # 添加地区过滤
            if region_codes:
                params['regionCode'] = region_codes[0]  # YouTube API只支持单地区
                
            # 发送请求
            response = self._make_request(self.SEARCH_ENDPOINT, params)
            
            if not response or 'items' not in response:
                break
                
            # 获取频道ID列表
            channel_ids = []
            for item in response.get('items', []):
                channel_id = item.get('snippet', {}).get('channelId')
                if channel_id:
                    channel_ids.append(channel_id)
            
            # 获取频道详情
            if channel_ids:
                channel_details = self._get_channel_details(channel_ids)
                
                for channel_data in channel_details:
                    creator = self._parse_channel_to_creator(channel_data)
                    if creator:
                        creators.append(creator)
                        collected_count += 1
                        
            # 检查是否有更多页面
            page_token = response.get('nextPageToken')
            if not page_token:
                break
                
            # 请求间隔
            time.sleep(self.settings.REQUEST_DELAY)
            
        self.logger.info(f"Collected {len(creators)} creators")
        return creators
    
    def _get_channel_details(self, channel_ids: List[str]) -> List[Dict]:
        """
        获取频道详情
        
        Args:
            channel_ids: 频道ID列表
            
        Returns:
            频道详情列表
        """
        if not channel_ids:
            return []
            
        # YouTube API限制每次最多50个ID
        all_details = []
        chunk_size = 50
        
        for i in range(0, len(channel_ids), chunk_size):
            chunk = channel_ids[i:i + chunk_size]
            
            params = {
                'part': 'snippet,statistics,contentDetails,brandingSettings',
                'id': ','.join(chunk)
            }
            
            response = self._make_request(self.CHANNELS_ENDPOINT, params)
            
            if response and 'items' in response:
                all_details.extend(response['items'])
                
            time.sleep(self.settings.REQUEST_DELAY)
            
        return all_details
    
    def _parse_channel_to_creator(self, channel_data: Dict) -> Optional[YouTubeCreator]:
        """
        将频道数据解析为创作者对象
        
        Args:
            channel_data: 频道数据
            
        Returns:
            创作者对象
        """
        try:
            snippet = channel_data.get('snippet', {})
            statistics = channel_data.get('statistics', {})
            branding = channel_data.get('brandingSettings', {})
            
            channel_id = channel_data.get('id')
            channel_name = snippet.get('title', '')
            description = snippet.get('description', '')
            custom_url = snippet.get('customUrl')
            thumbnail_url = snippet.get('thumbnails', {}).get('default', {}).get('url')
            banner_url = branding.get('image', {}).get('bannerExternalUrl')
            
            # 解析统计数据
            stats = ChannelStatistics(
                total_views=int(statistics.get('viewCount', 0)),
                subscriber_count=int(statistics.get('subscriberCount', 0)),
                video_count=int(statistics.get('videoCount', 0)),
                view_per_subscriber=0,
                subscriber_growth_rate=0,
                view_growth_rate=0,
                average_views_per_video=0,
                engagement_rate=0
            )
            
            # 计算派生统计
            if stats.subscriber_count > 0:
                stats.view_per_subscriber = stats.total_views / stats.subscriber_count
                
            if stats.video_count > 0:
                stats.average_views_per_video = stats.total_views / stats.video_count
                
            # 提取联系信息
            contact_info = None
            if branding.get('channel'):
                email = self._extract_email(description)
                twitter = self._extract_twitter(description)
                
                contact_info = ContactInfo(
                    email=email,
                    business_email=branding['channel'].get('businessEmail'),
                    twitter=twitter,
                    website=branding['channel'].get('links', [{}])[0].get('url') if branding['channel'].get('links') else None
                )
                
            # 解析活跃度数据
            activity_data = self._get_activity_data(channel_id)
            
            # 确定创作者状态
            status = self._determine_status(activity_data, stats)
            
            # 创建创作者对象
            creator = YouTubeCreator(
                channel_id=channel_id,
                channel_name=channel_name,
                custom_url=custom_url,
                description=description,
                thumbnail_url=thumbnail_url,
                banner_url=banner_url,
                statistics=stats,
                contact_info=contact_info,
                activity_data=activity_data,
                status=status,
                content_category=ContentCategory.GAMING,
                pc_game_focus=self._is_pc_game_channel(description),
                tags=self._extract_tags(channel_data)
            )
            
            return creator
            
        except Exception as e:
            self.logger.error(f"Error parsing channel data: {e}")
            return None
    
    def _get_activity_data(self, channel_id: str) -> Optional[ActivityData]:
        """
        获取频道活跃度数据
        
        Args:
            channel_id: 频道ID
            
        Returns:
            活跃度数据
        """
        # 获取最近视频列表
        params = {
            'part': 'snippet,statistics',
            'channelId': channel_id,
            'order': 'date',
            'maxResults': 50,
            'type': 'video'
        }
        
        response = self._make_request(self.VIDEOS_ENDPOINT, params)
        
        if not response or 'items' not in response:
            return None
            
        videos = response['items']
        
        # 计算活跃度指标
        now = datetime.now()
        last_30_days = now - timedelta(days=30)
        last_90_days = now - timedelta(days=90)
        
        videos_last_30_days = 0
        videos_last_90_days = 0
        last_video_date = None
        
        for video in videos:
            published_at_str = video.get('snippet', {}).get('publishedAt')
            if published_at_str:
                published_at = datetime.fromisoformat(published_at_str.replace('Z', '+00:00'))
                
                if published_at >= last_30_days:
                    videos_last_30_days += 1
                    
                if published_at >= last_90_days:
                    videos_last_90_days += 1
                    
                if last_video_date is None or published_at > last_video_date:
                    last_video_date = published_at
                    
        # 计算上传频率
        days_in_period = 90
        total_videos = videos_last_90_days
        upload_frequency = (total_videos / days_in_period) * 7  # 每周视频数
        avg_days_between = days_in_period / total_videos if total_videos > 0 else 0
        
        return ActivityData(
            last_video_date=last_video_date,
            total_videos_last_30_days=videos_last_30_days,
            total_videos_last_90_days=videos_last_90_days,
            upload_frequency_per_week=upload_frequency,
            average_days_between_uploads=avg_days_between
        )
    
    def _extract_email(self, text: str) -> Optional[str]:
        """
        从文本中提取邮箱地址
        
        Args:
            text: 文本
            
        Returns:
            邮箱地址
        """
        import re
        
        email_pattern = r'[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}'
        match = re.search(email_pattern, text)
        
        return match.group(0) if match else None
    
    def _extract_twitter(self, text: str) -> Optional[str]:
        """
        从文本中提取Twitter用户名
        
        Args:
            text: 文本
            
        Returns:
            Twitter用户名
        """
        import re
        
        twitter_pattern = r'@([A-Za-z0-9_]{1,15})'
        match = re.search(twitter_pattern, text)
        
        return match.group(1) if match else None
    
    def _determine_status(self, 
                         activity_data: ActivityData, 
                         stats: ChannelStatistics) -> CreatorStatus:
        """
        确定创作者状态
        
        Args:
            activity_data: 活跃度数据
            stats: 统计数据
            
        Returns:
            创作者状态
        """
        if not activity_data:
            return CreatorStatus.UNKNOWN
            
        # 检查最近是否有视频发布
        if activity_data.is_active_in_last_days(self.settings.MIN_ACTIVE_DAYS):
            return CreatorStatus.ACTIVE
        else:
            return CreatorStatus.INACTIVE
    
    def _is_pc_game_channel(self, description: str) -> bool:
        """
        判断是否为PC游戏频道
        
        Args:
            description: 频道描述
            
        Returns:
            是否为PC游戏频道
        """
        pc_keywords = ['pc', 'computer', 'steam', 'gaming', 'gamer', 'gaming']
        description_lower = description.lower()
        
        return any(keyword in description_lower for keyword in pc_keywords)
    
    def _extract_tags(self, channel_data: Dict) -> List[str]:
        """
        提取频道标签
        
        Args:
            channel_data: 频道数据
            
        Returns:
            标签列表
        """
        # 从topicCategories提取标签
        topic_categories = channel_data.get('topicDetails', {}).get('topicCategories', [])
        
        tags = []
        for category in topic_categories:
            # 从URL中提取标签名
            tag = category.split('/')[-1]
            tags.append(tag.replace('_', ' '))
            
        return tags
    
    def collect_creator_by_id(self, channel_id: str) -> Optional[YouTubeCreator]:
        """
        根据ID采集单个创作者数据
        
        Args:
            channel_id: 频道ID
            
        Returns:
            创作者对象
        """
        details = self._get_channel_details([channel_id])
        
        if details:
            return self._parse_channel_to_creator(details[0])
            
        return None
    
    def collect_creators_by_list(self, channel_ids: List[str]) -> List[YouTubeCreator]:
        """
        根据ID列表批量采集创作者数据
        
        Args:
            channel_ids: 频道ID列表
            
        Returns:
            创作者列表
        """
        creators = []
        
        # 分批处理
        chunk_size = 50
        for i in range(0, len(channel_ids), chunk_size):
            chunk = channel_ids[i:i + chunk_size]
            details = self._get_channel_details(chunk)
            
            for detail in details:
                creator = self._parse_channel_to_creator(detail)
                if creator:
                    creators.append(creator)
                    
        return creators
