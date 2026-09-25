---
slug: introducing-envoy-ai-gateway
title: Introducing Envoy AI Gateway
description: Open collaboration to bring AI Gateway features to the Envoy community
authors: [missberg]
tags: [news]
image: /img/og/blog/introducing-envoy-ai-gateway.png
---

**The industry is embracing Generative AI functionality, and we need to evolve how we handle traffic on an industry-wide scale. Keeping AI traffic handling features exclusive to enterprise licenses is counterproductive to the industry’s needs. This approach limits incentives to a single commercial entity and its customers. Even single-company open-source initiatives do not promote open multi-company collaboration.**

<!-- truncate -->

A shared challenge like this presents an opportunity for open collaboration to build the necessary features. We believe bringing together different use cases and requirements through open collaboration will lead to better solutions and accelerate innovation. The industry will benefit from diverse expertise and experiences by openly collaborating on software across companies and industries.

That is why Tetrate and Bloomberg have started an open collaboration to bring critical features for this new era of Gen AI integration. Collaborating openly in the Envoy community, bringing AI traffic handling features to Envoy, via Envoy Gateway and Envoy Proxy.

## Why we need AI traffic handling features

What makes traffic to LLM models different from traditional API traffic?

On the surface it appears similar. Traffic comes from a client app that is making an API request, and this request has to get to the provider that hosts the LLM model.

However, it is different. Managing LLM traffic from multiple apps, to multiple LLM providers, introduces new and different challenges where traditional API Gateway features fall short.

For example, traditional rate-limiting based on number of requests doesn’t work for controlling usage of LLM providers as they’re computationally complex services. To measure usage LLM providers tokenize the words in the request message and response message, and count the number of tokens used. This count gives a good approximation of the computational complexity and cost of serving the request.

Beyond controlling usage of LLMs there are many more challenges relating to ease of integration and high-availability architectures. It’s no longer enough to just optimize for quality of service alone, adopters must consider costs of usage in real time. As adopters of Gen AI look for Gateway solutions to handle these challenges for their system, they often find the necessary features locked behind enterprise licenses.

## Three key MVP features

Now, let’s look at how handling AI traffic poses new challenges for Gateways. There are several features we discussed together with our collaborators at Bloomberg, and together we decided on three key features for the MVP:

- **Usage Limiting** – to control LLM usage based on word tokens
- **Unified API** – to simplify client integration with multiple LLM providers
- **Upstream Authorization** – to configure Authorization to multiple upstream LLM providers
  What other features are you looking for? Get in touch with us to share your use case and define the future of Envoy AI Gateway.

We are really excited about these features being part of Envoy. They will benefit those integrating with LLM providers and, ultimately, also Gateway users for general API request traffic.

When it comes to AI Gateway features, we have chosen to collaborate and build within the CNCF Envoy project because we believe multi-company, open-source projects benefit the entire industry by enabling innovation without creating single vendor risk.
