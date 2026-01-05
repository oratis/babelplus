# 使用最新的 1.42 镜像（通常自带 PHP 8.2+）
FROM mediawiki:1.42

# 必须修改端口为 8080 以适应 Cloud Run
RUN sed -i 's/80/8080/g' /etc/apache2/sites-available/000-default.conf /etc/apache2/ports.conf

# 确保文件夹权限正确
RUN chown -R www-data:www-data /var/www/html

EXPOSE 8080
