import os
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import List
from llama_index.core.data_structs import Node
from llama_index.core.schema import NodeWithScore
from llama_index.postprocessor.dashscope_rerank import DashScopeRerank

if not os.getenv("DASHSCOPE_API_KEY"):
    raise ValueError("DASHSCOPE_API_KEY environment variable not set.")

app = FastAPI(title="Reranker Service")
dashscope_rerank = DashScopeRerank(top_n=500) 

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
    接收一个查询和一组文档，返回经过DashScope重排后的文档列表。
    """
    try:
        # 将请求数据转换为LlamaIndex需要的格式
        # 注意：NodeWithScore可以不传入score，它会被reranker忽略
        nodes = [
            NodeWithScore(node=Node(text=doc.text, id_=doc.id))
            for doc in request.documents
        ]

        # 调用Re-ranker核心方法
        reranked_nodes = dashscope_rerank.postprocess_nodes(
            nodes, 
            query_str=request.query
        )

        # 封装返回结果
        response = [
            RerankedDocument(id=node.node.id_, rerank_score=node.score)
            for node in reranked_nodes
        ]
        
        return response
    except Exception as e:
        # 在生产环境中，这里应该有更详细的日志记录
        print(f"Reranker service error: {e}")
        raise HTTPException(status_code=500, detail=str(e)) 