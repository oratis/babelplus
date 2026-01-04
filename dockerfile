# 使用官方 MediaWiki 镜像作为基础
FROM mediawiki:1.41

# 将当前目录的代码复制到容器中（可选，如果你有自定义配置）
# COPY . /var/www/html

# 暴露端口
EXPOSE 8080

# 覆盖默认的 Apache 端口配置以适应 Cloud Run（Cloud Run 强制使用 8080）
RUN sed -i 's/Listen 80/Listen 8080/' /etc/apache2/ports.conf
RUN sed -i 's/:80/:8080/' /etc/apache2/sites-available/000-default.conf
