# 使用官方 MediaWiki 镜像作为基础
FROM mediawiki:1.41

# Cloud Run 必须监听 8080 端口，而 MediaWiki 默认是 80
# 我们需要修改 Apache 配置
RUN sed -i 's/80/8080/g' /etc/apache2/sites-available/000-default.conf /etc/apache2/ports.conf

# (可选) 如果你需要安装额外的 PHP 扩展，可以在这里添加
# RUN apt-get update && apt-get install -y ...

# 确保文件夹权限正确
RUN chown -R www-data:www-data /var/www/html
