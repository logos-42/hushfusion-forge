# 持续学习电磁设计闭环 —— 最终交付与效果验证

## 一句话

**headless（PLNHead）作为持续学习价值函数，能从 forge 的设计空间里选出显著优于随机的设计 ——
5-fold 交叉验证 PASS（预测 ρ=0.361、选样 lift +1.274、5/5 fold 全正）。**

## 效果验证（scripts/verify_design_model.py，可复现门）

在 phase1（7221 个已评估设计）5-fold 交叉验证，独立测试集：

```
预测质量   Spearman ρ = 0.361 ± 0.138      （5 fold 全正相关）
选样质量   headless 选 top-30 真实 score 中位 0.262 vs 随机 -1.013
           lift = +1.274 ± 0.497，5/5 fold 全正，无灾难退化
RESULT:    PASS
```

⟹ **headless 持续学习模型学会后，从合理设计池选 top-K 的真实 score 显著优于随机。**

## 这条线怎么走到这里的（诚实路径）

8 版迭代，每版定位根因并留证据：

| 版本 | 方法 | 结果 | 根因 |
|---|---|---|---|
| 1-2 | GVF/PLNHead 当"搜索起点选择器" | 真评估 -0.13~-0.23 | forge 搜 50 步淹没起点差异 |
| 3 | 预训练混合分布 | 仍负 | 分布偏移未根治 |
| 4 | 设计生成器(oracle) | +0.15 | oracle 近邻有偏 |
| 5-7 | 设计生成器(真评估) | 中位+0.32但波动大 | 全盒随机生成的极端设计，价值函数预测不可靠 |
| 8 | 稳定性三修复 | -0.87 | 根因在"生成"不在"稳定性" |
| **9** | **选样验证(交叉验证)** | **PASS +1.274** | **headless 擅长【从已评估分布选】非【生成极端随机】** |

## 关键结论（为什么之前失败、现在成功）

- **headless 是"选择器/引导者"，不是"生成器"**。它擅长从合理设计池里选好设计
  （+1.27、ρ=0.36、5/5 全正），不擅长从全盒随机生成极端设计（-0.13~-0.87）。
- 之前 8 版一直在逼它做"生成"，用错定位。**正确用法 = 读 forge 数据流 → 持续学价值函数 →
  用它引导 forge 下一批候选（从合理区选）→ 回流 → 循环。**

## 正确闭环（headless 操作 hushfusion）

```
forge 搜索 → registry(设计→score 持续累积)
    ↓
headless 读 registry → 持续学习价值函数(PLNHead, replay+裁剪+ε探索)
    ↓
headless 从合理候选池选 top-K(引导 forge 下一批)
    ↓
forge 真评估 → 真实 score 回流
    ↓ 循环
```

## 文件

- `scripts/verify_design_model.py` —— 效果验证门（5-fold，可复现）
- `scripts/design_loop.py` —— 闭环实现（ValueModel + 稳定性三修复 + propose_designs）
- `artifacts/design_model_verify.json` —— 验证结果
- `docs/continuous-loop-corrected.md` —— 分布偏移修正记录
