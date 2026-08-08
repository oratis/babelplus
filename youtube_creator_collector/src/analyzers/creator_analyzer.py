"""
数据分析模块
============

对创作者数据进行深度分析。
"""

from typing import List, Dict, Optional, Tuple
from datetime import datetime, timedelta
from dataclasses import dataclass, field
from statistics import mean, stdev, median
import logging

from config.settings import Settings
from models.creator import YouTubeCreator, ChannelStatistics, ActivityData, RegionData

@dataclass
class AnalysisResult:
    """分析结果"""
    creator_id: str
    creator_name: str
    scores: Dict[str, float] = field(default_factory=dict)
    metrics: Dict[str, any] = field(default_factory=dict)
    flags: List[str] = field(default_factory=list)
    recommendations: List[str] = field(default_factory=list)
    analyzed_at: datetime = field(default_factory=datetime.now)

class CreatorAnalyzer:
    """
    创作者数据分析器
    
    负责对创作者数据进行多维度分析
    """
    
    def __init__(self, settings: Settings):
        """
        初始化分析器
        
        Args:
            settings: 配置对象
        """
        self.settings = settings
        self.logger = logging.getLogger(__name__)
        
    def analyze_creators(self,
                        creators: List[YouTubeCreator],
                        criteria: Dict = None) -> List[YouTubeCreator]:
        """
        批量分析创作者并筛选符合条件的数据
        
        Args:
            creators: 创作者列表
            criteria: 筛选条件
            
        Returns:
            符合筛选条件的创作者列表
        """
        if criteria is None:
            criteria = {
                'active_days': self.settings.MIN_ACTIVE_DAYS,
                'min_view_ratio': self.settings.MIN_VIEW_RATIO,
                'regions': self.settings.TARGET_REGIONS,
                'has_email': self.settings.MIN_EMAIL_AVAILABLE
            }
            
        self.logger.info(f"Analyzing {len(creators)} creators with criteria: {criteria}")
        
        qualified_creators = []
        analysis_results = []
        
        for creator in creators:
            # 分析单个创作者
            result = self._analyze_creator(creator, criteria)
            
            if result.metrics.get('meets_all_criteria', False):
                qualified_creators.append(creator)
                
            analysis_results.append(result)
            
        # 生成分析报告
        self._generate_analysis_report(analysis_results)
        
        self.logger.info(f"Qualified {len(qualified_creators)} creators")
        return qualified_creators
    
    def _analyze_creator(self,
                        creator: YouTubeCreator,
                        criteria: Dict) -> AnalysisResult:
        """
        分析单个创作者
        
        Args:
            creator: 创作者对象
            criteria: 筛选条件
            
        Returns:
            分析结果
        """
        result = AnalysisResult(
            creator_id=creator.channel_id,
            creator_name=creator.channel_name
        )
        
        # 计算各项评分
        activity_score = self._calculate_activity_score(creator)
        stability_score = self._calculate_stability_score(creator)
        engagement_score = self._calculate_engagement_score(creator)
        region_score = self._calculate_region_score(creator, criteria['regions'])
        email_score = self._calculate_email_score(creator)
        
        result.scores = {
            'activity': activity_score,
            'stability': stability_score,
            'engagement': engagement_score,
            'region_relevance': region_score,
            'contact_availability': email_score,
            'overall': 0
        }
        
        # 计算综合评分
        weights = {
            'activity': 0.25,
            'stability': 0.20,
            'engagement': 0.20,
            'region_relevance': 0.20,
            'contact_availability': 0.15
        }
        
        overall_score = sum(
            result.scores[key] * weight 
            for key, weight in weights.items()
        )
        result.scores['overall'] = round(overall_score, 2)
        
        # 检查筛选条件
        checks = {
            'active_recently': self._check_activity(creator, criteria['active_days']),
            'region_ratio_met': self._check_region_ratio(creator, criteria['regions'], criteria['min_view_ratio']),
            'has_email': self._check_email(creator) if criteria.get('has_email', True) else True,
            'stable_performance': self._check_stability(creator)
        }
        
        result.metrics = {
            'checks': checks,
            'meets_all_criteria': all(checks.values()),
            'subscriber_count': creator.statistics.subscriber_count if creator.statistics else 0,
            'total_views': creator.statistics.total_views if creator.statistics else 0,
            'activity_score': activity_score,
            'region_view_percentage': creator.get_target_region_view_percentage(criteria['regions'])
        }
        
        # 生成标记和建议
        result.flags = self._generate_flags(creator, checks)
        result.recommendations = self._generate_recommendations(creator, result.scores, checks)
        
        return result
    
    def _calculate_activity_score(self, creator: YouTubeCreator) -> float:
        """
        计算活跃度评分 (0-100)
        
        Args:
            creator: 创作者对象
            
        Returns:
            活跃度评分
        """
        if not creator.activity_data:
            return 0
            
        score = creator.activity_data.get_activity_score()
        return round(score, 2)
    
    def _calculate_stability_score(self, creator: YouTubeCreator) -> float:
        """
        计算稳定性评分 (0-100)
        
        Args:
            creator: 创作者对象
            
        Returns:
            稳定性评分
        """
        if not creator.statistics:
            return 0
            
        stability = creator.statistics.calculate_stability_score(self.settings.STABILITY_WINDOW_DAYS)
        return round(stability, 2)
    
    def _calculate_engagement_score(self, creator: YouTubeCreator) -> float:
        """
        计算互动率评分 (0-100)
        
        Args:
            creator: 创作者对象
            
        Returns:
            互动率评分
        """
        if not creator.statistics:
            return 0
            
        engagement = creator.statistics.engagement_rate
        
        # 转化互动率为评分
        # 假设10%互动率为满分，低于1%为0分
        if engagement >= 10:
            return 100
        elif engagement < 1:
            return 0
        else:
            score = (engagement - 1) / 9 * 100
            return round(min(100, score), 2)
    
    def _calculate_region_score(self, 
                               creator: YouTubeCreator, 
                               target_regions: List[str]) -> float:
        """
        计算地区相关性评分 (0-100)
        
        Args:
            creator: 创作者对象
            target_regions: 目标地区列表
            
        Returns:
            地区评分
        """
        if not creator.statistics or not creator.region_data:
            return 0
            
        region_percentage = creator.get_target_region_view_percentage(target_regions)
        return round(region_percentage * 100, 2)
    
    def _calculate_email_score(self, creator: YouTubeCreator) -> float:
        """
        计算邮箱可用性评分 (0-100)
        
        Args:
            creator: 创作者对象
            
        Returns:
            邮箱评分
        """
        if not creator.contact_info:
            return 0
            
        if creator.contact_info.has_valid_email():
            return 100
            
        return 0
    
    def _check_activity(self, creator: YouTubeCreator, days: int) -> bool:
        """
        检查活跃度
        
        Args:
            creator: 创作者对象
            days: 天数
            
        Returns:
            是否活跃
        """
        if not creator.activity_data:
            return False
            
        return creator.activity_data.is_active_in_last_days(days)
    
    def _check_region_ratio(self,
                           creator: YouTubeCreator,
                           target_regions: List[str],
                           min_ratio: float) -> bool:
        """
        检查地区占比
        
        Args:
            creator: 创作者对象
            target_regions: 目标地区列表
            min_ratio: 最小占比
            
        Returns:
            是否满足要求
        """
        if not creator.statistics:
            return False
            
        region_ratio = creator.get_target_region_view_percentage(target_regions)
        return region_ratio >= min_ratio
    
    def _check_email(self, creator: YouTubeCreator) -> bool:
        """
        检查邮箱可用性
        
        Args:
            creator: 创作者对象
            
        Returns:
            是否有邮箱
        """
        if not creator.contact_info:
            return False
            
        return creator.contact_info.has_valid_email()
    
    def _check_stability(self, creator: YouTubeCreator) -> bool:
        """
        检查稳定性
        
        Args:
            creator: 创作者对象
            
        Returns:
            是否稳定
        """
        if not creator.statistics:
            return False
            
        return creator.statistics.is_stable(self.settings.GROWTH_THRESHOLD)
    
    def _generate_flags(self, 
                       creator: YouTubeCreator, 
                       checks: Dict[str, bool]) -> List[str]:
        """
        生成标记列表
        
        Args:
            creator: 创作者对象
            checks: 检查结果
            
        Returns:
            标记列表
        """
        flags = []
        
        if not checks['active_recently']:
            flags.append('inactive')
            
        if not checks['region_ratio_met']:
            flags.append('low_region_relevance')
            
        if not checks['has_email']:
            flags.append('no_contact_email')
            
        if not checks['stable_performance']:
            flags.append('unstable_growth')
            
        if creator.statistics:
            if creator.statistics.subscriber_count >= 1000000:
                flags.append('mega_creator')
            elif creator.statistics.subscriber_count >= 100000:
                flags.append('large_creator')
            elif creator.statistics.subscriber_count >= 10000:
                flags.append('mid_tier_creator')
                
        return flags
    
    def _generate_recommendations(self,
                                 creator: YouTubeCreator,
                                 scores: Dict[str, float],
                                 checks: Dict[str, bool]) -> List[str]:
        """
        生成建议列表
        
        Args:
            creator: 创作者对象
            scores: 评分
            checks: 检查结果
            
        Returns:
            建议列表
        """
        recommendations = []
        
        # 基于评分给出建议
        if scores['activity'] < 50:
            recommendations.append('Consider creators with more consistent upload schedule')
            
        if scores['stability'] < 50:
            recommendations.append('Creator shows irregular growth patterns')
            
        if scores['engagement'] < 30:
            recommendations.append('Low engagement rate - may indicate audience disconnect')
            
        if scores['region_relevance'] < 60:
            recommendations.append('Target region audience is limited')
            
        if scores['contact_availability'] < 100:
            recommendations.append('Contact information incomplete')
            
        # 基于创作者规模给出建议
        if creator.statistics:
            if creator.statistics.subscriber_count >= 1000000:
                recommendations.append('Mega creator - high cost, wide reach')
            elif creator.statistics.subscriber_count >= 100000:
                recommendations.append('Large creator - balanced reach and engagement')
                
        return recommendations
    
    def _generate_analysis_report(self, results: List[AnalysisResult]) -> Dict:
        """
        生成分析报告
        
        Args:
            results: 分析结果列表
            
        Returns:
            报告字典
        """
        report = {
            'generated_at': datetime.now().isoformat(),
            'total_analyzed': len(results),
            'qualified_count': sum(1 for r in results if r.metrics.get('meets_all_criteria', False)),
            'score_distribution': {
                'overall': self._calculate_score_distribution([r.scores['overall'] for r in results]),
                'activity': self._calculate_score_distribution([r.scores['activity'] for r in results]),
                'stability': self._calculate_score_distribution([r.scores['stability'] for r in results]),
                'engagement': self._calculate_score_distribution([r.scores['engagement'] for r in results]),
            },
            'common_flags': self._count_common_flags(results),
            'region_distribution': {}
        }
        
        self.logger.info(f"Analysis Report: {report['qualified_count']}/{report['total_analyzed']} qualified")
        
        return report
    
    def _calculate_score_distribution(self, scores: List[float]) -> Dict:
        """
        计算评分分布
        
        Args:
            scores: 评分列表
            
        Returns:
            分布统计
        """
        if not scores:
            return {}
            
        return {
            'min': round(min(scores), 2),
            'max': round(max(scores), 2),
            'mean': round(mean(scores), 2),
            'median': round(median(scores), 2),
            'std': round(stdev(scores), 2) if len(scores) > 1 else 0
        }
    
    def _count_common_flags(self, results: List[AnalysisResult]) -> Dict[str, int]:
        """
        统计常见标记
        
        Args:
            results: 分析结果列表
            
        Returns:
            标记计数
        """
        flag_counts = {}
        
        for result in results:
            for flag in result.flags:
                flag_counts[flag] = flag_counts.get(flag, 0) + 1
                
        return dict(sorted(flag_counts.items(), key=lambda x: x[1], reverse=True))
    
    def compare_creators(self, 
                        creators: List[YouTubeCreator],
                        metrics: List[str] = None) -> List[Dict]:
        """
        比较多个创作者
        
        Args:
            creators: 创作者列表
            metrics: 比较指标
            
        Returns:
            比较结果列表
        """
        if metrics is None:
            metrics = ['subscriber_count', 'total_views', 'engagement_rate', 'activity_score']
            
        comparisons = []
        
        for creator in creators:
            comparison = {
                'channel_id': creator.channel_id,
                'channel_name': creator.channel_name
            }
            
            for metric in metrics:
                if metric == 'subscriber_count' and creator.statistics:
                    comparison['subscribers'] = creator.statistics.subscriber_count
                elif metric == 'total_views' and creator.statistics:
                    comparison['views'] = creator.statistics.total_views
                elif metric == 'engagement_rate' and creator.statistics:
                    comparison['engagement'] = creator.statistics.engagement_rate
                elif metric == 'activity_score' and creator.activity_data:
                    comparison['activity'] = creator.activity_data.get_activity_score()
                    
            comparisons.append(comparison)
            
        # 按订阅者排序
        comparisons.sort(key=lambda x: x.get('subscribers', 0), reverse=True)
        
        return comparisons
    
    def rank_creators(self,
                     creators: List[YouTubeCreator],
                     ranking_criteria: Dict = None) -> List[Tuple[YouTubeCreator, float]]:
        """
        创作者排名
        
        Args:
            creators: 创作者列表
            ranking_criteria: 排名标准
            
        Returns:
            排名列表（创作者, 评分）
        """
        if ranking_criteria is None:
            ranking_criteria = {
                'activity': 0.3,
                'engagement': 0.3,
                'stability': 0.2,
                'region_relevance': 0.2
            }
            
        ranked = []
        
        for creator in creators:
            if not creator.statistics:
                continue
                
            # 计算综合评分
            activity = self._calculate_activity_score(creator)
            engagement = self._calculate_engagement_score(creator)
            stability = self._calculate_stability_score(creator)
            region = self._calculate_region_score(creator, self.settings.TARGET_REGIONS)
            
            score = sum(
                score * weight 
                for score, weight in zip(
                    [activity, engagement, stability, region],
                    ranking_criteria.values()
                )
            )
            
            ranked.append((creator, round(score, 2)))
            
        # 按评分降序排序
        ranked.sort(key=lambda x: x[1], reverse=True)
        
        return ranked
