import os
from contextlib import asynccontextmanager
from typing import List, Optional, Union

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from sentence_transformers import SentenceTransformer


class EmbeddingRequest(BaseModel):
    input: Union[str, List[str]]
    model: Optional[str] = None
    dimensions: Optional[int] = Field(default=None, gt=0)


class EmbeddingDatum(BaseModel):
    embedding: List[float]
    index: int


class EmbeddingResponse(BaseModel):
    data: List[EmbeddingDatum]


MODEL: SentenceTransformer | None = None


def get_model_id() -> str:
    return os.getenv("MODEL_ID", "google/embeddinggemma-300m")


def load_model() -> SentenceTransformer:
    global MODEL
    if MODEL is None:
        MODEL = SentenceTransformer(
            get_model_id(),
            token=os.getenv("HF_TOKEN"),
            trust_remote_code=True,
        )
    return MODEL


@asynccontextmanager
async def lifespan(_: FastAPI):
    load_model()
    yield


app = FastAPI(lifespan=lifespan)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/v1/embeddings", response_model=EmbeddingResponse)
def embeddings(req: EmbeddingRequest) -> EmbeddingResponse:
    model_name = get_model_id()
    if req.model and req.model != model_name:
        raise HTTPException(
            status_code=400,
            detail=f"requested model {req.model!r} does not match configured model {model_name!r}",
        )

    items = [req.input] if isinstance(req.input, str) else req.input
    if not items:
        return EmbeddingResponse(data=[])

    model = load_model()
    kwargs = {
        "sentences": items,
        "normalize_embeddings": True,
        "convert_to_numpy": True,
    }
    if req.dimensions is not None:
        kwargs["truncate_dim"] = req.dimensions

    vectors = model.encode(**kwargs)
    data = [
        EmbeddingDatum(embedding=[float(v) for v in vec.tolist()], index=i)
        for i, vec in enumerate(vectors)
    ]
    return EmbeddingResponse(data=data)
