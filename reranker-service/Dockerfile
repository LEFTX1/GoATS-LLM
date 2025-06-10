# 1. 使用官方的、轻量的Python基础镜像
FROM python:3.10-slim

# 2. 设置工作目录
WORKDIR /app

# 3. 复制依赖文件并安装
# (先复制requirements.txt可以利用Docker的层缓存机制，只要依赖不变，就不需要重新安装)
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 4. 复制应用代码
COPY . .

# 5. 暴露服务端口
EXPOSE 8000

# 6. 容器启动时运行的命令
# 使用uvicorn启动FastAPI应用，--host 0.0.0.0 确保服务在容器外部可访问
CMD ["uvicorn", "reranker_service:app", "--host", "0.0.0.0", "--port", "8000"] 