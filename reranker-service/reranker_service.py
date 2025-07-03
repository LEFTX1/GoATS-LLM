# 导入必要的库
import os
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import List
import dashscope
from http import HTTPStatus

# 检查环境变量中是否设置了DASHSCOPE_API_KEY
if not os.getenv("DASHSCOPE_API_KEY"):
    # 如果未设置，则抛出ValueError异常
    raise ValueError("DASHSCOPE_API_KEY environment variable not set.")

# 创建一个FastAPI应用实例，并设置标题
app = FastAPI(title="Reranker Service")

# 定义文档的数据模型，包含id和text两个字段
class Document(BaseModel):
    id: str  # 文档的唯一标识符
    text: str # 文档的文本内容

# 定义重排请求的数据模型
class RerankRequest(BaseModel):
    query: str  # 用户的查询语句
    documents: List[Document] # 待重排的文档列表

# 定义重排后文档的数据模型
class RerankedDocument(BaseModel):
    id: str  # 文档的唯一标识符
    rerank_score: float # 文档的重排得分

# 定义一个POST路由"/rerank"，用于接收重排请求
@app.post("/rerank", response_model=List[RerankedDocument])
async def rerank_documents(request: RerankRequest):
    """
    接收一个查询和一组文档，使用DashScope gte-rerank-v2模型进行重排。
    """
    try:
        # 1. 准备API调用所需的文档列表，提取每个文档的文本内容
        doc_texts = [doc.text for doc in request.documents]

        # 2. 调用DashScope的TextReRank API进行重排
        resp = dashscope.TextReRank.call(
            model="gte-rerank-v2", # 指定使用的模型
            query=request.query, # 传入查询语句
            documents=doc_texts, # 传入待重排的文档文本列表
            top_n=len(doc_texts) # 设置返回的文档数量为原始文档数量，以确保所有文档都被排序
        )

        # 3. 检查API响应状态码
        if resp.status_code == HTTPStatus.OK:
            # 如果API调用成功，处理返回结果
            # 创建一个空列表，用于存放重排后的文档
            reranked_docs = []
            # 遍历API返回的每个结果
            for result in resp.output.results:
                # 'index'字段表示原始文档列表中的索引
                original_doc_index = result['index']
                # 使用该索引从原始请求的文档列表中找到对应的文档
                original_doc = request.documents[original_doc_index]
                
                # 将重排后的文档信息添加到列表中
                reranked_docs.append(
                    RerankedDocument(
                        id=original_doc.id, # 保留原始文档的id
                        rerank_score=result['relevance_score'] # 记录重排后的相关性得分
                    )
                )
            # 返回重排后的文档列表
            return reranked_docs
        else:
            # 如果API调用失败，记录错误信息并返回500错误
            error_message = f"DashScope API Error: {resp.message}"
            print(error_message) # 在服务器端打印错误信息
            # 抛出HTTPException，将错误信息返回给客户端
            raise HTTPException(
                status_code=500, 
                detail=error_message
            )

    except Exception as e:
        # 捕获其他未预料到的异常
        print(f"Reranker service internal error: {e}") # 在服务器端打印异常信息
        # 抛出HTTPException，将异常信息作为详情返回给客户端
        raise HTTPException(status_code=500, detail=str(e)) 