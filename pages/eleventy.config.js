export default function (eleventyConfig) {
  eleventyConfig.setChokidarConfig({
		usePolling: true,
		interval: 500,
	});
  
  eleventyConfig.addPassthroughCopy("src/styles.css");
  eleventyConfig.addPassthroughCopy({ "../assets/preview.png": "assets/preview.png" });

  return {
    dir: {
      input: "src",
      output: "dist",
      includes: "_includes"
    }
  };
}
