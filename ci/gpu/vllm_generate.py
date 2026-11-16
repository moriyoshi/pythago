# vLLM offline generation: V1 engine process, torch.compile, CUDA graphs.
from vllm import LLM, SamplingParams
llm = LLM(model="Qwen/Qwen2.5-0.5B-Instruct", gpu_memory_utilization=0.25, max_model_len=2048)
params = SamplingParams(temperature=0.0, max_tokens=24)
prompts = ["The capital of France is", "1 + 1 =", "Go is a programming language that"]
first = [o.outputs[0].text for o in llm.generate(prompts, params)]
again = [o.outputs[0].text for o in llm.generate(prompts, params)]
assert "Paris" in first[0], first
assert first == again, (first, again)
print("ok", repr(first[0]))
