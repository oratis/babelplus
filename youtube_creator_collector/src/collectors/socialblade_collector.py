"""
SocialBlade数据采集器
=====================

从SocialBlade采集创作者统计数据。
"""

import time
import re
from typing import List, Dict, Optional
from datetime import datetime, timedelta
import logging
import requests
from bs4 import BeautifulSoup

from config.settings import Settings
from models.creator import YouTubeCreator, ChannelStatistics, RegionData

class SocialBladeCollector:
    """
    SocialBlade数据采集器
    
    负责从SocialBlade网站采集创作者统计数据
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
        
        # SocialBlade URL配置
        self.BASE_URL = self.settings.SOCIALBLADE_BASE_URL
        self.YOUTUBE_PROFILE_URL = f"{self.BASE_URL}/youtube/user"
        self.YOUTUBE_CHANNEL_URL = f"{self.BASE_URL}/youtube/channel"
    
    def enrich_creator_data(self, 
                           creators: List[YouTubeCreator],
                           max_workers: int = 5) -> List[YouTubeCreator]:
        """
        批量丰富创作者数据
        
        Args:
            creators: 创作者列表
            max_workers: 最大并发数
            
        Returns:
            丰富后的创作者列表
        """
        self.logger.info(f"Enriching data for {len(creators)} creators")
        
        enriched_count = 0
        
        for creator in creators:
            try:
                # 获取SocialBlade数据
                sb_data = self._fetch_socialblade_data(creator.channel_id)
                
                if sb_data:
                    # 更新创作者数据
                    self._update_creator_from_socialblade(creator, sb_data)
                    enriched_count += 1
                    
                # 请求间隔
                time.sleep(self.settings.SOCIALBLADE_REQUEST_DELAY)
                
            except Exception as e:
                self.logger.warning(f"Failed to enrich creator {creator.channel_id}: {e}")
                
        self.logger.info(f"Enriched {enriched_count}/{len(creators)} creators")
        return creators
    
    def _fetch_socialblade_data(self, channel_id: str) -> Optional[Dict]:
        """
        获取SocialBlade数据
        
        Args:
            channel_id: YouTube频道ID
            
        Returns:
            SocialBlade数据字典
        """
        # 构建URL
        url = f"{self.YOUTUBE_CHANNEL_URL}/{channel_id}"
        
        try:
            headers = self.settings.get_socialblade_headers()
            response = self.session.get(url, headers=headers, timeout=self.settings.REQUEST_TIMEOUT)
            
            if response.status_code != 200:
                self.logger.warning(f"Failed to fetch SocialBlade data: HTTP {response.status_code}")
                return None
                
            # 解析HTML
            soup = BeautifulSoup(response.text, 'html.parser')
            
            # 提取数据
            data = self._parse_socialblade_page(soup)
            
            return data
            
        except Exception as e:
            self.logger.error(f"Error fetching SocialBlade data: {e}")
            return None
    
    def _parse_socialblade_page(self, soup: BeautifulSoup) -> Dict:
        """
        解析SocialBlade页面
        
        Args:
            soup: BeautifulSoup对象
            
        Returns:
            解析后的数据
        """
        data = {}
        
        try:
            # 提取用户统计信息
            stats_div = soup.find('div', {'id': 'socialblade-youtube-channel-header'})
            
            if stats_div:
                # 订阅者数量
                subscriber_elem = stats_div.find('span', {'id': 'youtube-stats-header-subs'})
                if subscriber_elem:
                    data['subscriber_count'] = self._parse_number(subscriber_elem.text)
                    
                # 总观看量
                views_elem = stats_div.find('span', {'id': 'youtube-stats-header-views'})
                if views_elem:
                    data['total_views'] = self._parse_number(views_elem.text)
                    
                # 视频数量
                videos_elem = stats_div.find('span', {'id': 'youtube-stats-header-videos'})
                if videos_elem:
                    data['video_count'] = self._parse_number(videos_elem.text)
            
            # 提取地区分布数据
            region_table = soup.find('table', {'id': 'socialblade-youtube-country-table'})
            
            if region_table:
                data['regions'] = self._parse_region_table(region_table)
                
            # 提取增长率数据
            growth_data = soup.find('div', {'id': 'youtube-stats-growth'})
            
            if growth_data:
                data['growth'] = self._parse_growth_data(growth_data)
                
        except Exception as e:
            self.logger.error(f"Error parsing SocialBlade page: {e}")
            
        return data
    
    def _parse_number(self, text: str) -> int:
        """
        解析数字文本
        
        Args:
            text: 数字文本（如 "1.2M", "345K"）
            
        Returns:
            整数
        """
        text = text.strip().upper().replace(',', '')
        
        multipliers = {
            'K': 1000,
            'M': 1000000,
            'B': 1000000000
        }
        
        for suffix, multiplier in multipliers.items():
            if suffix in text:
                try:
                    return int(float(text.replace(suffix, '')) * multiplier)
                except ValueError:
                    return 0
                    
        try:
            return int(text)
        except ValueError:
            return 0
    
    def _parse_region_table(self, table) -> List[Dict]:
        """
        解析地区分布表
        
        Args:
            table: 表格元素
            
        Returns:
            地区数据列表
        """
        regions = []
        
        try:
            rows = table.find_all('tr')
            
            for row in rows[1:]:  # 跳过表头
                cells = row.find_all('td')
                
                if len(cells) >= 3:
                    region_code = cells[0].get('data-country', '')
                    region_name = cells[0].text.strip()
                    view_percentage = self._parse_percentage(cells[1].text)
                    subscriber_percentage = self._parse_percentage(cells[2].text)
                    
                    regions.append({
                        'region_code': region_code,
                        'region_name': region_name,
                        'view_percentage': view_percentage,
                        'subscriber_percentage': subscriber_percentage
                    })
                    
        except Exception as e:
            self.logger.error(f"Error parsing region table: {e}")
            
        return regions
    
    def _parse_percentage(self, text: str) -> float:
        """
        解析百分比文本
        
        Args:
            text: 百分比文本（如 "25.5%"）
            
        Returns:
            浮点数（0-100）
        """
        try:
            return float(text.strip().replace('%', ''))
        except ValueError:
            return 0.0
    
    def _parse_growth_data(self, element) -> Dict:
        """
        解析增长率数据
        
        Args:
            element: 增长数据元素
            
        Returns:
            增长率数据
        """
        growth = {
            'subscriber_daily': 0,
            'subscriber_weekly': 0,
            'subscriber_monthly': 0,
            'views_daily': 0,
            'views_weekly': 0,
            'views_monthly': 0
        }
        
        try:
            # 提取增长率文本
            growth_text = element.text
            
            # 使用正则表达式提取数值
            patterns = [
                (r'(\d+,?\d*)\s*new\s*subscribers/day', 'subscriber_daily'),
                (r'(\d+,?\d*)\s*new\s*subscribers/week', 'subscriber_weekly'),
                (r'(\d+,?\d*)\s*new\s*subscribers/month', 'subscriber_monthly'),
                (r'(\d+,?\d*)\s*views/day', 'views_daily'),
                (r'(\d+,?\d*)\s*views/week', 'views_weekly'),
                (r'(\d+,?\d*)\s*views/month', 'views_monthly')
            ]
            
            for pattern, key in patterns:
                match = re.search(pattern, growth_text, re.IGNORECASE)
                if match:
                    growth[key] = self._parse_number(match.group(1))
                    
        except Exception as e:
            self.logger.error(f"Error parsing growth data: {e}")
            
        return growth
    
    def _update_creator_from_socialblade(self, 
                                        creator: YouTubeCreator,
                                        sb_data: Dict) -> None:
        """
        使用SocialBlade数据更新创作者对象
        
        Args:
            creator: 创作者对象
            sb_data: SocialBlade数据
        """
        # 更新统计数据
        if creator.statistics:
            if 'subscriber_count' in sb_data:
                creator.statistics.subscriber_count = sb_data['subscriber_count']
                
            if 'total_views' in sb_data:
                creator.statistics.total_views = sb_data['total_views']
                
            if 'video_count' in sb_data:
                creator.statistics.video_count = sb_data['video_count']
                
            # 更新派生统计
            if creator.statistics.subscriber_count > 0:
                creator.statistics.view_per_subscriber = \
                    creator.statistics.total_views / creator.statistics.subscriber_count
                    
            if creator.statistics.video_count > 0:
                creator.statistics.average_views_per_video = \
                    creator.statistics.total_views / creator.statistics.video_count
                    
        # 更新地区数据
        if 'regions' in sb_data:
            creator.region_data = []
            
            for region_info in sb_data['regions']:
                region = RegionData(
                    region_code=region_info.get('region_code', ''),
                    region_name=region_info.get('region_name', ''),
                    view_count=int(creator.statistics.total_views * 
                                  region_info.get('view_percentage', 0) / 100) if creator.statistics else 0,
                    subscriber_count=int(creator.statistics.subscriber_count * 
                                        region_info.get('subscriber_percentage', 0) / 100) if creator.statistics else 0,
                    view_percentage=region_info.get('view_percentage', 0)
                )
                creator.region_data.append(region)
                
        # 更新增长率
        if 'growth' in sb_data:
            growth = sb_data['growth']
            
            if creator.statistics and creator.statistics.subscriber_count > 0:
                creator.statistics.subscriber_growth_rate = \
                    growth.get('subscriber_daily', 0) / creator.statistics.subscriber_count
                    
            if creator.statistics and creator.statistics.total_views > 0:
                creator.statistics.view_growth_rate = \
                    growth.get('views_daily', 0) / creator.statistics.total_views
                    
        # 更新时间戳
        creator.last_updated = datetime.now()
    
    def search_creators_by_keyword(self, 
                                  keyword: str,
                                  category: str = 'gaming',
                                  limit: int = 50) -> List[Dict]:
        """
        搜索创作者
        
        Args:
            keyword: 搜索关键词
            category: 类别
            limit: 结果限制
            
        Returns:
            创作者信息列表
        """
        # SocialBlade的搜索功能有限，这里是模拟实现
        search_url = f"{self.BASE_URL}/youtube/search/{keyword}"
        
        try:
            headers = self.settings.get_socialblade_headers()
            response = self.session.get(search_url, headers=headers, timeout=self.settings.REQUEST_TIMEOUT)
            
            if response.status_code == 200:
                soup = BeautifulSoup(response.text, 'html.parser')
                # 解析搜索结果
                return self._parse_search_results(soup, limit)
                
        except Exception as e:
            self.logger.error(f"Search failed: {e}")
            
        return []
    
    def _parse_search_results(self, soup: BeautifulSoup, limit: int) -> List[Dict]:
        """
        解析搜索结果
        
        Args:
            soup: BeautifulSoup对象
            limit: 结果限制
            
        Returns:
            搜索结果列表
        """
        results = []
        
        try:
            # 查找创作者列表
            creator_items = soup.find_all('div', {'class': 'creator-item'})[:limit]
            
            for item in creator_items:
                name_elem = item.find('a', {'class': 'creator-name'})
                views_elem = item.find('span', {'class': 'total-views'})
                subs_elem = item.find('span', {'class': 'subscriber-count'})
                
                if name_elem:
                    results.append({
                        'name': name_elem.text.strip(),
                        'url': name_elem.get('href', ''),
                        'views': self._parse_number(views_elem.text) if views_elem else 0,
                        'subscribers': self._parse_number(subs_elem.text) if subs_elem else 0
                    })
                    
        except Exception as e:
            self.logger.error(f"Error parsing search results: {e}")
            
        return results
