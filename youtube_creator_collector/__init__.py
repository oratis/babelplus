# -*- coding: utf-8 -*-
"""
YouTube PC游戏创作者数据收集工具
=================================

目标地区: 美国、加拿大、英国、澳大利亚、新西兰、新加坡
筛选条件:
- Top 10000创作者
- 30天内活跃
- 目标地区播放占比>60%
- 具备合作邮箱
"""

__version__ = "1.0.0"
__author__ = "YouTube Creator Analytics Team"

from .main import YouTubeCreatorCollector

__all__ = ['YouTubeCreatorCollector']
