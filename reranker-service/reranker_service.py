import os
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import List
import dashscope
from http import HTTPStatus

if not os.getenv("DASHSCOPE_API_KEY"):
    raise ValueError("DASHSCOPE_API_KEY environment variable not set.")

app = FastAPI(title="Reranker Service")

class Document(BaseModel):
    id: str
    text: str

class RerankRequest(BaseModel):
    query: str
    documents: List[Document]

class RerankedDocument(BaseModel):
    id: str
    rerank_score: float

@app.post("/rerank", response_model=List[RerankedDocument])
async def rerank_documents(request: RerankRequest):
    """
    接收一个查询和一组文档，使用DashScope gte-rerank-v2模型进行重排。
    """
    try:
        # 1. 准备API调用所需的文档列表 (List[str])
        doc_texts = [doc.text for doc in request.documents]

        # 2. 直接调用DashScope TextReRank API
        resp = dashscope.TextReRank.call(
            model="gte-rerank-v2",
            query=request.query,
            documents=doc_texts,
            top_n=len(doc_texts) # 确保返回所有文档的排序结果
        )

        # 3. 检查API响应并处理结果
        if resp.status_code == HTTPStatus.OK:
            # 创建一个列表来保存最终排序后的文档
            # output.results中的结果已经按相关性从高到低排序
            reranked_docs = []
            for result in resp.output.results:
                # 'index' 字段指的是原始输入documents列表中的索引
                original_doc_index = result['index']
                # 使用该索引从原始请求中找到对应的文档以获取其ID
                original_doc = request.documents[original_doc_index]
                
                reranked_docs.append(
                    RerankedDocument(
                        id=original_doc.id,
                        rerank_score=result['relevance_score']
                    )
                )
            return reranked_docs
        else:
            # 如果API调用失败，记录错误并返回500
            error_message = f"DashScope API Error: {resp.message}"
            print(error_message)
            raise HTTPException(
                status_code=500, 
                detail=error_message
            )

    except Exception as e:
        # 捕获其他意外错误
        print(f"Reranker service internal error: {e}")
        raise HTTPException(status_code=500, detail=str(e)) 