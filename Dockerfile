# 使用最新的 1.42 镜像（通常自带 PHP 8.2+）
FROM mediawiki:1.42

# 1. 复制当前目录下的所有文件（包括 LocalSettings.php）到容器目录
COPY . /var/www/html/

# 2. 必须修改端口为 8080
RUN sed -i 's/80/8080/g' /etc/apache2/sites-available/000-default.conf /etc/apache2/ports.conf

# 3. 设置权限（非常重要，否则 MediaWiki 无法读取）
RUN chown -R www-data:www-data /var/www/html/

EXPOSE 8080
