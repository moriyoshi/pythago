# vLLM pooling runner (embeddings).
import math
from vllm import LLM
llm = LLM(model="intfloat/multilingual-e5-small", runner="pooling", gpu_memory_utilization=0.15, enforce_eager=True)
v = [o.outputs.embedding for o in llm.embed(["query: what is the capital of France?",
     "passage: Paris is the capital of France.", "passage: Go is a programming language."])]
cos = lambda a, b: sum(x * y for x, y in zip(a, b)) / math.sqrt(sum(x * x for x in a) * sum(y * y for y in b))
assert len(v[0]) == 384 and cos(v[0], v[1]) > cos(v[0], v[2])
print("ok")
